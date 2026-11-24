// Package imagearchive transfers Docker archives outside the Agent wire protocol.
package imagearchive

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

const MaxBytes int64 = 8 << 30
const Timeout = 15 * time.Minute
const maxTransfers = 8

var (
	ErrUnavailable = errors.New("image transfer is unavailable")
	ErrTooLarge    = errors.New("image archive exceeds the transfer limit")
)

type Endpoint interface {
	Send(context.Context, string, io.ReadCloser) error
	Receive(context.Context, string, func(io.Reader) error) error
}

type Relay struct {
	mu        sync.Mutex
	transfers map[string]*transfer
}

type transfer struct {
	source           chan io.ReadCloser
	done             chan struct{}
	once             sync.Once
	err              error
	sender, receiver bool
}

func (r *Relay) join(id string, send bool) (*transfer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.transfers[id]
	if t == nil {
		if len(r.transfers) >= maxTransfers {
			return nil, ErrUnavailable
		}
		if r.transfers == nil {
			r.transfers = make(map[string]*transfer)
		}
		t = &transfer{source: make(chan io.ReadCloser, 1), done: make(chan struct{})}
		r.transfers[id] = t
	}
	if send && t.sender || !send && t.receiver {
		return nil, ErrUnavailable
	}
	if send {
		t.sender = true
	} else {
		t.receiver = true
	}
	return t, nil
}

func (r *Relay) finish(id string, t *transfer, err error) {
	t.once.Do(func() {
		t.err = err
		close(t.done)
		r.mu.Lock()
		delete(r.transfers, id)
		r.mu.Unlock()
	})
}

// Abort releases a transfer waiting on a node operation that has failed.
func (r *Relay) Abort(id string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	t := r.transfers[id]
	r.mu.Unlock()
	if t != nil {
		r.finish(id, t, ErrUnavailable)
	}
}

func (r *Relay) Send(ctx context.Context, id string, source io.ReadCloser) error {
	defer source.Close()
	t, err := r.join(id, true)
	if err != nil {
		return err
	}
	t.source <- source
	select {
	case <-ctx.Done():
		r.finish(id, t, ctx.Err())
	case <-t.done:
	}
	return t.err
}

func (r *Relay) Receive(ctx context.Context, id string, consume func(io.Reader) error) error {
	t, err := r.join(id, false)
	if err != nil {
		return err
	}
	var source io.ReadCloser
	select {
	case <-ctx.Done():
		r.finish(id, t, ctx.Err())
		return t.err
	case <-t.done:
		return t.err
	case source = <-t.source:
	}
	stop := context.AfterFunc(ctx, func() { source.Close() })
	defer stop()
	err = consumeArchive(source, consume)
	r.finish(id, t, err)
	return t.err
}

func consumeArchive(source io.Reader, consume func(io.Reader) error) error {
	reader := &archiveReader{Reader: source, remaining: MaxBytes}
	if err := consume(reader); err != nil {
		return err
	}
	_, err := io.Copy(io.Discard, reader)
	return err
}

type archiveReader struct {
	io.Reader
	remaining    int64
	limitReached bool
}

func (r *archiveReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.limitReached {
		return 0, ErrTooLarge
	}
	if r.remaining == 0 {
		var b [1]byte
		n, err := r.Reader.Read(b[:])
		if n != 0 {
			r.limitReached = true
			return 0, ErrTooLarge
		}
		return 0, err
	}
	n, err := r.Reader.Read(p[:min(int64(len(p)), r.remaining)])
	r.remaining -= int64(n)
	return n, err
}
