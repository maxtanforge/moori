// Package execstream pairs an interactive browser terminal with one
// authenticated node relay. The control plane forwards frames without
// buffering terminal bytes and enforces only pairing support and a
// concurrency cap.
package execstream

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// WriteTimeout bounds a single frame write in either direction.
	WriteTimeout = 10 * time.Second
	// PingInterval keeps bridged connections alive through idle periods.
	PingInterval = 25 * time.Second
	// PongTimeout drops a peer that stops answering keepalive pings.
	PongTimeout = 60 * time.Second

	// MaxSessions caps concurrent terminal sessions process-wide.
	MaxSessions = 8
	maxMessage  = 64 << 10

	MinCols    = 2
	MaxCols    = 1024
	MinRows    = 2
	MaxRows    = 512
	ColDefault = 80
	RowDefault = 24
)

var (
	// openTimeout bounds how long a browser terminal waits for the node relay.
	openTimeout = 5 * time.Second
	// ackTimeout bounds the node's reply after receiving exec parameters.
	ackTimeout = 10 * time.Second
)

var (
	ErrBusy        = errors.New("too many live container terminals")
	ErrUnavailable = errors.New("container terminal is unavailable")
	ErrInvalid     = errors.New("invalid container terminal session")
)

var containerIDPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// ValidContainer reports whether a container identifier matches the full
// hex IDs the console hands out.
func ValidContainer(id string) bool { return containerIDPattern.MatchString(id) }

// OpenParams describes the exec session a browser asked for.
type OpenParams struct {
	Container string `json:"container"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
}

// Normalize clamps the terminal size to the supported bounds.
func (p OpenParams) Normalize() OpenParams {
	if p.Cols < MinCols || p.Cols > MaxCols {
		p.Cols = ColDefault
	}
	if p.Rows < MinRows || p.Rows > MaxRows {
		p.Rows = RowDefault
	}
	return p
}

// Validate rejects sessions the node must not receive.
func (p OpenParams) Validate() error {
	if !ValidContainer(p.Container) {
		return ErrInvalid
	}
	if p.Cols != 0 && (p.Cols < MinCols || p.Cols > MaxCols) {
		return ErrInvalid
	}
	if p.Rows != 0 && (p.Rows < MinRows || p.Rows > MaxRows) {
		return ErrInvalid
	}
	return nil
}

// Ack is the node's reply to the exec parameters.
type Ack struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// EOF marks the end of the exec process in a relayed control frame.
type EOF struct {
	EOF      bool `json:"eof"`
	ExitCode int  `json:"exit_code,omitempty"`
}

// Resize adjusts the remote terminal size.
type Resize struct {
	Resize Size `json:"resize"`
}

// Size carries terminal dimensions.
type Size struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

// readyMessage tells the browser the session state after pairing.
type readyMessage struct {
	Ready bool   `json:"ready"`
	Error string `json:"error,omitempty"`
}

// Connection is one side of a bridged terminal WebSocket. Writes are
// serialized; each direction has a single reader.
type Connection struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

// NewConnection wraps an upgraded WebSocket for terminal framing.
func NewConnection(c *websocket.Conn) *Connection {
	c.SetReadLimit(maxMessage)
	return &Connection{conn: c}
}

func (c *Connection) send(messageType int, data []byte) error {

	if len(data) > maxMessage {
		return errors.New("terminal frame exceeds the size limit")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.conn.SetWriteDeadline(time.Now().Add(WriteTimeout)); err != nil {
		return err
	}
	return c.conn.WriteMessage(messageType, data)
}

// SendJSON delivers a control frame to the peer.
func (c *Connection) SendJSON(payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return c.send(websocket.TextMessage, data)
}

// Relay forwards a data frame verbatim, preserving its text/binary kind.
func (c *Connection) Relay(kind int, data []byte) error {
	if kind != websocket.TextMessage && kind != websocket.BinaryMessage {
		return errors.New("unexpected terminal frame kind")
	}
	return c.send(kind, data)
}

func (c *Connection) ping() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.conn.SetWriteDeadline(time.Now().Add(WriteTimeout)); err != nil {
		return err
	}
	return c.conn.WriteMessage(websocket.PingMessage, nil)
}

// Close delivers a close frame and drops the underlying connection.
func (c *Connection) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(time.Second))
	_ = c.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	_ = c.conn.Close()
}

// keepalive pings the peer; inbound pongs extend the read deadline.
func keepalive(ctx context.Context, c *Connection) {
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(PongTimeout))
	})
	_ = c.conn.SetReadDeadline(time.Now().Add(PongTimeout))
	ticker := time.NewTicker(PingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if err := c.ping(); err != nil {
			return
		}
	}
}

type frame struct {
	kind int
	data []byte
}

// readPump is the single reader for one connection. Control frames such as
// pings are answered by the websocket library while this loop is blocked.
func readPump(ctx context.Context, c *Connection, out chan<- frame) {
	defer close(out)
	for {
		kind, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		select {
		case out <- frame{kind: kind, data: data}:
		case <-ctx.Done():
			return
		}
	}
}

type peer struct {
	agent      string
	generation int64
}

func (p peer) valid() bool {
	return p.agent != "" && len(p.agent) <= 128 && p.generation > 0
}

type agentWait struct {
	wakeup chan struct{}
}

type session struct {
	id  string
	seq int64
	OpenParams
	peer peer

	mu      sync.Mutex
	claimed bool
	agent   *Connection
	browser *Connection

	bound   chan struct{} // closed when the browser connection is attached
	paired  chan struct{} // closed when a node relay claims the session
	ack     chan error    // carries the node's exec acknowledgement
	started chan struct{} // closed once the node confirmed the exec session
	done    chan struct{} // closed by teardown; distinguishes a clean end

	start  sync.Once
	finish sync.Once
	err    error
}

func (s *session) attach(conn *Connection, browser bool) {
	s.mu.Lock()
	if browser {
		s.browser = conn
	} else {
		s.agent = conn
	}
	s.mu.Unlock()
}

func (s *session) peerConn(browser bool) *Connection {
	s.mu.Lock()
	defer s.mu.Unlock()
	if browser {
		return s.browser
	}
	return s.agent
}

// teardown ends the session once, waking a browser that is still pairing
// or waiting for the node's acknowledgement.
func (s *session) teardown(b *Broker, cause error) {
	s.finish.Do(func() {
		s.err = cause
		close(s.done)
		b.mu.Lock()
		if b.pending != nil {
			delete(b.pending, s.id)
		}
		b.slots--
		b.mu.Unlock()
		select {
		case s.ack <- cause:
		default:
		}
		s.mu.Lock()
		agent, browser := s.agent, s.browser
		s.mu.Unlock()
		if agent != nil {
			agent.Close()
		}
		if browser != nil {
			browser.Close()
		}
	})
}

// A Broker's zero value is ready for use. Its callers authenticate each
// peer before passing the node ID and credential generation.
type Broker struct {
	mu      sync.Mutex
	slots   int
	seq     int64
	pending map[string]*session
	waiting map[peer]*agentWait
}

// Begin reserves a terminal session slot for a browser request. The caller
// must Connect the session after upgrading its WebSocket, or Cancel the
// reservation when the upgrade fails.
func (b *Broker) Begin(agent string, generation int64, params OpenParams) (*Session, error) {
	p := peer{agent, generation}
	if !p.valid() || params.Validate() != nil {
		return nil, ErrInvalid
	}
	params = params.Normalize()
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.slots >= MaxSessions {
		return nil, ErrBusy
	}
	if b.pending == nil {
		b.pending = make(map[string]*session)
	}
	if b.waiting == nil {
		b.waiting = make(map[peer]*agentWait)
	}
	b.seq++
	s := &session{
		id:         hex.EncodeToString(random[:]),
		seq:        b.seq,
		OpenParams: params,
		peer:       p,
		bound:      make(chan struct{}),
		paired:     make(chan struct{}),
		ack:        make(chan error, 1),
		started:    make(chan struct{}),
		done:       make(chan struct{}),
	}
	b.slots++
	b.pending[s.id] = s
	if wait := b.waiting[p]; wait != nil {
		close(wait.wakeup)
		delete(b.waiting, p)
	}
	return &Session{session: s, broker: b}, nil
}

// Accept serves one node relay connection. It blocks until a browser pairs,
// relays the node's frames for the session, and returns when it ends. A nil
// return means the exec process ended and the session was relayed cleanly.
func (b *Broker) Accept(ctx context.Context, agent string, generation int64, conn *Connection) error {
	p := peer{agent, generation}
	if !p.valid() || conn == nil {
		return ErrInvalid
	}
	go keepalive(ctx, conn)
	frames := make(chan frame, 4)
	go readPump(ctx, conn, frames)

	wait := &agentWait{wakeup: make(chan struct{})}
	b.mu.Lock()
	if b.waiting == nil {
		b.waiting = make(map[peer]*agentWait)
	}
	// A newer relay supersedes an idle older one for the same node.
	if stale := b.waiting[p]; stale != nil {
		close(stale.wakeup)
	}
	b.waiting[p] = wait
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		if b.waiting[p] == wait {
			delete(b.waiting, p)
		}
		b.mu.Unlock()
	}()

	s := b.claim(ctx, p, wait, frames)
	if s == nil {
		return ErrUnavailable
	}
	defer s.teardown(b, errors.New("node relay disconnected"))
	s.attach(conn, false)

	// Deliver exec parameters, then wait for the node's acknowledgement.
	if err := conn.SendJSON(s.OpenParams); err != nil {
		s.teardown(b, err)
		return err
	}
	var ackErr error
	select {
	case msg, ok := <-frames:
		if !ok {
			ackErr = ErrUnavailable
			break
		}
		if msg.kind == websocket.TextMessage {
			var reply Ack
			if json.Unmarshal(msg.data, &reply) == nil {
				if reply.OK {
					break // confirmed: parameters accepted
				}
				if reply.Error != "" {
					ackErr = errors.New(reply.Error)
				}
			}
		}
		if ackErr == nil {
			ackErr = ErrInvalid
		}
	case <-ctx.Done():
		s.teardown(b, ctx.Err())
		return ctx.Err()
	}
	if ackErr != nil {
		// Tell the browser before tearing the connections down.
		if browser := s.peerConn(true); browser != nil {
			_ = browser.SendJSON(readyMessage{Error: ackErr.Error()})
		}
		s.teardown(b, ackErr)
		return ackErr
	}
	s.ack <- nil

	// The browser attaches its connection right after the upgrade; wait for
	// it so no node output is lost before the relay pump starts.
	select {
	case <-s.bound:
	case <-ctx.Done():
		s.teardown(b, ctx.Err())
		return ctx.Err()
	}
	s.start.Do(func() { close(s.started) })

	browser := s.peerConn(true)
	for {
		msg, ok := <-frames
		if !ok {
			select {
			case <-s.done:
				return s.err // the session already ended cleanly
			default:
				s.teardown(b, errors.New("node relay disconnected"))
				return ErrUnavailable
			}
		}
		if msg.kind == websocket.TextMessage {
			var eof EOF
			if json.Unmarshal(msg.data, &eof) == nil && eof.EOF {
				_ = browser.Relay(msg.kind, msg.data)
				s.teardown(b, nil)
				return nil
			}
		}
		if err := browser.Relay(msg.kind, msg.data); err != nil {
			s.teardown(b, err)
			return err
		}
	}
}

// claim reserves the oldest pending session for the node and closes paired
// so the browser side proceeds.
func (b *Broker) claim(ctx context.Context, p peer, wait *agentWait, frames <-chan frame) *session {
	for {
		b.mu.Lock()
		var found *session
		for _, s := range b.pending {
			if !s.claimed && s.peer == p && (found == nil || s.seq < found.seq) {
				found = s
			}
		}
		if found != nil {
			found.claimed = true
			close(found.paired)
			b.mu.Unlock()
			return found
		}
		wakeup := wait.wakeup
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil
		case <-wakeup:
		case _, ok := <-frames:
			_ = ok
			// Any frame or disconnect before pairing ends the idle relay.
			return nil
		}
	}
}

// Session is one reserved browser terminal. Its methods belong to the HTTP
// handler that upgrades the browser's WebSocket.
type Session struct {
	*session
	broker *Broker
}

// Started closes once the node confirmed the exec session.
func (s *Session) Started() <-chan struct{} { return s.started }

// Cancel releases a reservation whose WebSocket upgrade failed.
func (s *Session) Cancel() { s.teardown(s.broker, ErrUnavailable) }

// Connect bridges the upgraded browser WebSocket until the session ends.
// It returns nil when the session finished cleanly.
func (s *Session) Connect(ctx context.Context, conn *Connection) error {
	if conn == nil {
		s.teardown(s.broker, ErrInvalid)
		return ErrInvalid
	}
	s.attach(conn, true)
	close(s.bound)
	go keepalive(ctx, conn)
	frames := make(chan frame, 16)
	go readPump(ctx, conn, frames)

	// Wait for a node relay, then for its exec acknowledgement.
	fail := func(cause error) error {
		_ = conn.SendJSON(readyMessage{Error: cause.Error()})
		s.teardown(s.broker, cause)
		return cause
	}
	timer := time.NewTimer(openTimeout)
	defer timer.Stop()
	select {
	case <-s.paired:
	case <-timer.C:
		return fail(ErrUnavailable)
	case <-ctx.Done():
		s.teardown(s.broker, ctx.Err())
		return ctx.Err()
	}
	timer.Reset(ackTimeout)
	select {
	case err := <-s.ack:
		if err != nil {
			// The relay normally notifies the browser before tearing down;
			// for a browser that attached late this resend still lands.
			return fail(err)
		}
	case <-timer.C:
		return fail(ErrUnavailable)
	case <-ctx.Done():
		s.teardown(s.broker, ctx.Err())
		return ctx.Err()
	}
	_ = conn.SendJSON(readyMessage{Ready: true})

	// Relay browser frames to the node until the session ends.
	agent := s.peerConn(false)
	for {
		select {
		case msg, ok := <-frames:
			if !ok {
				select {
				case <-s.done:
					return s.err // the session already ended cleanly
				default:
					s.teardown(s.broker, errors.New("browser terminal disconnected"))
					return ErrUnavailable
				}
			}
			if err := agent.Relay(msg.kind, msg.data); err != nil {
				s.teardown(s.broker, err)
				return err
			}
		case <-ctx.Done():
			s.teardown(s.broker, ctx.Err())
			return ctx.Err()
		}
	}
}
