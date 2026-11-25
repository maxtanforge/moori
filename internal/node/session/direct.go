package session

import (
	"context"
	"github.com/maxtanforge/moori/internal/transport/agentwire"
)

type messageChannel interface {
	Send(agentwire.Message) error
	Receive() (agentwire.Message, error)
	Close() error
}

// The session exchanges one request at a time. The buffered response permits
// cancellation without spawning a second worker or opening a network listener.
type directChannel struct {
	ctx       context.Context
	cancel    context.CancelFunc
	reply     func(context.Context, agentwire.Message) (agentwire.Message, error)
	responses chan agentwire.Message
}

func newDirectChannel(ctx context.Context, reply func(context.Context, agentwire.Message) (agentwire.Message, error)) *directChannel {
	ctx, cancel := context.WithCancel(ctx)
	return &directChannel{ctx: ctx, cancel: cancel, reply: reply, responses: make(chan agentwire.Message, 1)}
}
func (c *directChannel) Send(m agentwire.Message) error {
	if err := c.ctx.Err(); err != nil {
		return err
	}
	response, err := c.reply(c.ctx, m)
	if err != nil {
		return err
	}
	select {
	case c.responses <- response:
		return nil
	case <-c.ctx.Done():
		return c.ctx.Err()
	}
}
func (c *directChannel) Receive() (agentwire.Message, error) {
	select {
	case m := <-c.responses:
		return m, nil
	case <-c.ctx.Done():
		return agentwire.Message{}, c.ctx.Err()
	}
}
func (c *directChannel) Close() error { c.cancel(); return nil }
