// Package logstream joins a browser log reader to one authenticated node
// publisher without buffering log bytes in the control plane.
package logstream

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/maxtanforge/moori/internal/core/nodeops"
)

const (
	OpenTimeout   = 3 * time.Second
	PollTimeout   = 25 * time.Second
	StreamTimeout = 30 * time.Minute

	maxTickets = 8
	pollRecent = 10 * time.Second
)

var (
	ErrUnavailable = errors.New("container log stream is unavailable")
	ErrBusy        = errors.New("too many live container log streams")
	ErrInvalid     = errors.New("invalid container log stream")
)

type Ticket struct {
	ID         string `json:"id"`
	Container  string `json:"container"`
	Tail       int    `json:"tail"`
	Timestamps bool   `json:"timestamps,omitempty"`
	AllLogRuns bool   `json:"all_log_runs,omitempty"`
}

// A Broker's zero value is ready for use. Its callers authenticate each peer
// before passing the node ID and credential generation.
type Broker struct {
	mu      sync.Mutex
	polls   map[peer]*pollState
	streams map[string]*stream
}

type peer struct {
	agent      string
	generation int64
}

func (p peer) valid() bool {
	return p.agent != "" && len(p.agent) <= 128 && p.generation > 0
}

type pollState struct {
	active int
	last   time.Time
	wakeup chan struct{}
}

type stream struct {
	Ticket
	peer       peer
	registered time.Time
	claimed    bool
	publisher  bool

	source chan io.ReadCloser
	done   chan struct{}
	once   sync.Once
	err    error
}

// Receive registers a pending request for the specific node, waits for the
// matching publisher, and passes its body directly to consume.
func (b *Broker) Receive(ctx context.Context, agent string, generation int64, container string, tail int, timestamps, allRuns bool, consume func(io.Reader) error) error {
	p := peer{agent, generation}
	req := nodeops.Request{Action: nodeops.ContainerLogs, Target: container, Tail: tail, Timestamps: timestamps}
	if !p.valid() || req.Validate() != nil || consume == nil {
		return ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if tail == 0 {
		tail = nodeops.DefaultLogLines
	}
	s, err := b.register(p, container, tail, timestamps, allRuns)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, StreamTimeout)
	defer cancel()
	opening, finishedOpening := context.WithTimeout(ctx, OpenTimeout)
	var reader io.ReadCloser
	select {
	case reader = <-s.source:
		err = opening.Err()
	case <-opening.Done():
		err = opening.Err()
	case <-s.done:
		err = s.err
	}
	finishedOpening()
	if err != nil || reader == nil {
		if err == nil {
			err = ErrUnavailable
		}
		b.finish(s, err)
		return s.err
	}
	stop := context.AfterFunc(ctx, func() { b.finish(s, ctx.Err()) })
	defer stop()
	err = consume(reader)
	if err == nil {
		err = ctx.Err()
	}
	b.finish(s, err)
	return s.err
}

func (b *Broker) register(p peer, container string, tail int, timestamps, allRuns bool) (*stream, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	id := hex.EncodeToString(random[:])
	now := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.prunePolls(now)
	state := b.polls[p]
	if state == nil {
		return nil, ErrUnavailable
	}
	if len(b.streams) >= maxTickets {
		return nil, ErrBusy
	}
	s := &stream{
		Ticket: Ticket{ID: id, Container: container, Tail: tail, Timestamps: timestamps, AllLogRuns: allRuns}, peer: p, registered: now,
		source: make(chan io.ReadCloser, 1), done: make(chan struct{}),
	}
	if b.streams == nil {
		b.streams = make(map[string]*stream)
	}
	b.streams[id] = s
	close(state.wakeup)
	state.wakeup = make(chan struct{})
	return s, nil
}

// Next establishes that a node can publish live logs and claims at most one
// pending ticket for that exact node ID and generation.
func (b *Broker) Next(ctx context.Context, agent string, generation int64) (Ticket, error) {
	p := peer{agent, generation}
	if !p.valid() {
		return Ticket{}, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, PollTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Ticket{}, err
	}
	now := time.Now()
	b.mu.Lock()
	b.prunePolls(now)
	if b.polls == nil {
		b.polls = make(map[peer]*pollState)
	}
	state := b.polls[p]
	if state == nil {
		state = &pollState{wakeup: make(chan struct{})}
		b.polls[p] = state
	}
	state.active++
	state.last = now
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		state.active--
		state.last = time.Now()
		b.mu.Unlock()
	}()

	for {
		b.mu.Lock()
		if err := ctx.Err(); err != nil {
			b.mu.Unlock()
			return Ticket{}, err
		}
		var found *stream
		for _, request := range b.streams {
			if !request.claimed && request.peer == p && (found == nil || request.registered.Before(found.registered)) {
				found = request
			}
		}
		if found != nil {
			found.claimed = true
			ticket := found.Ticket
			b.mu.Unlock()
			return ticket, nil
		}
		wakeup := state.wakeup
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			return Ticket{}, ctx.Err()
		case <-wakeup:
		}
	}
}

// Send consumes a claimed ticket once. The body must support interruption by
// Close while its Read method is blocked, as net/http request adapters do.
func (b *Broker) Send(ctx context.Context, agent string, generation int64, id string, body io.ReadCloser) error {
	if body == nil {
		return ErrInvalid
	}
	defer body.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	stream := b.streams[id]
	if stream == nil || !stream.claimed || stream.publisher || stream.peer != (peer{agent, generation}) {
		b.mu.Unlock()
		return ErrUnavailable
	}
	stream.publisher = true
	b.mu.Unlock()
	// Deliver outside the broker lock. A receiver whose open window has just
	// expired finishes its stream through the same mutex; sending the body
	// while holding the lock could block that finish forever and freeze every
	// log stream in the process.
	select {
	case stream.source <- body:
	case <-ctx.Done():
		b.finish(stream, ctx.Err())
		return stream.err
	case <-stream.done:
		return stream.err
	}
	select {
	case <-ctx.Done():
		b.finish(stream, ctx.Err())
	case <-stream.done:
	}
	return stream.err
}

func (b *Broker) prunePolls(now time.Time) {
	for p, state := range b.polls {
		if state.active == 0 && now.Sub(state.last) >= pollRecent {
			delete(b.polls, p)
		}
	}
}

func (b *Broker) finish(s *stream, err error) {
	s.once.Do(func() {
		b.mu.Lock()
		delete(b.streams, s.ID)
		b.mu.Unlock()
		s.err = err
		close(s.done)
	})
}
