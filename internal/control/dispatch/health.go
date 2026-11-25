package dispatch

import (
	"context"
	"log/slog"
	"time"

	"github.com/maxtanforge/moori/internal/control/observability"
	"github.com/maxtanforge/moori/internal/core/telemetry"
	"github.com/maxtanforge/moori/internal/transport/agentwire"
)

const healthWriteTimeout = 250 * time.Millisecond

func (s Session) hostSample(ctx context.Context, peer agentwire.Peer, m agentwire.Message) (agentwire.Message, error) {
	var sample telemetry.Sample
	if err := agentwire.Decode(m, agentwire.KindHostSample, &sample); err != nil {
		return agentwire.Message{}, err
	}
	if s.Health != nil {
		writeCtx, cancel := context.WithTimeout(ctx, healthWriteTimeout)
		defer cancel()
		if err := s.Health.RecordSample(writeCtx, peer.ID, peer.Generation, sample, time.Now()); err != nil {
			slog.Debug("host sample not retained", "agent", peer.ID, "err", err)
		}
	}
	return agentwire.Encode(agentwire.KindHostSampleAck, struct{}{})
}

func (s Session) RecordContact(ctx context.Context, peer agentwire.Peer, at time.Time) {
	for _, store := range []*observability.Store{s.NodeState, s.Health} {
		if store == nil {
			continue
		}
		writeCtx, cancel := context.WithTimeout(ctx, healthWriteTimeout)
		if err := store.RecordContact(writeCtx, peer.ID, peer.Generation, at); err != nil {
			slog.Debug("Agent contact not retained", "agent", peer.ID, "err", err)
		}
		cancel()
	}
}
