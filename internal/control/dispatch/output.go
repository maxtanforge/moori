package dispatch

import (
	"context"
	"errors"
	"log/slog"

	"github.com/maxtanforge/moori/internal/platform/execlog"
	"github.com/maxtanforge/moori/internal/transport/agentwire"
)

func (s Session) output(ctx context.Context, peer agentwire.Peer, m agentwire.Message) (agentwire.Message, error) {
	var entry execlog.Entry
	if err := agentwire.Decode(m, agentwire.KindOutput, &entry); err != nil {
		return agentwire.Message{}, err
	}
	if entry.Execution == "" || len(entry.Execution) > 128 {
		return agentwire.Message{}, execlog.ErrInvalid
	}
	ack := agentwire.OutputAck{ID: entry.Execution}
	// Output failures never prevent durable state reports on this channel.
	allowed, err := s.Store.CanAttachOutput(ctx, entry.Execution, peer.ID, peer.Generation)
	switch {
	case err != nil:
		ack.Retry = true
		slog.Warn("deployment output authorization unavailable", "execution", entry.Execution, "err", err)
	case !allowed:
	case s.Logs == nil:
		ack.Retry = true
		slog.Warn("deployment output storage unavailable", "execution", entry.Execution)
	default:
		err = s.Logs.Put(entry)
		ack.Retained = err == nil
		ack.Retry = err != nil && !errors.Is(err, execlog.ErrInvalid) && !errors.Is(err, execlog.ErrConflict)
		if err != nil {
			slog.Warn("deployment output storage failed", "execution", entry.Execution, "retry", ack.Retry, "err", err)
		}
	}
	return agentwire.Encode(agentwire.KindOutputAck, ack)
}
