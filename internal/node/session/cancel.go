package session

import (
	"context"
	"database/sql"
	"errors"
	"sync"

	"github.com/maxtanforge/moori/internal/core/deployment"
	"github.com/maxtanforge/moori/internal/transport/agentwire"
)

type workerState struct {
	mu     sync.Mutex
	id     string
	cancel context.CancelFunc
}

func (w *workerState) begin(id string, cancel context.CancelFunc) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.id, w.cancel = id, cancel
}

func (w *workerState) end() { w.begin("", nil) }

func (w *workerState) busy() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.id != ""
}

func (w *workerState) signal(id string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.id != id || w.cancel == nil {
		return false
	}
	w.cancel()
	return true
}

func (c Client) cancelExecution(ctx context.Context, ch messageChannel, active *workerState, m agentwire.Message) error {
	var command agentwire.Cancel
	if err := agentwire.Decode(m, agentwire.KindCancel, &command); err != nil {
		return err
	}
	if command.Execution == "" || len(command.Execution) > 128 {
		return errors.New("invalid cancellation execution ID")
	}
	state, err := c.Session.Ledger.CancelAccepted(ctx, command.Execution)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	uncertain := !state.Terminal()
	if state == deployment.Executing || state == deployment.Verifying {
		uncertain = !active.signal(command.Execution)
	}
	reply, err := agentwire.Encode(agentwire.KindCancelReply, agentwire.CancelReply{Execution: command.Execution, Uncertain: uncertain})
	if err != nil {
		return err
	}
	result, err := exchange(ch, reply)
	if err != nil {
		return err
	}
	var ack agentwire.Ack
	if err := agentwire.Decode(result, agentwire.KindCancelAck, &ack); err != nil {
		return err
	}
	if ack.ID != command.Execution {
		return errors.New("cancellation acknowledgement does not match execution")
	}
	return nil
}
