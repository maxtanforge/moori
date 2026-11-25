package dispatch

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/maxtanforge/moori/internal/control/observability"
	"github.com/maxtanforge/moori/internal/node/localinv"
	"github.com/maxtanforge/moori/internal/transport/agentwire"
)

func (s Session) nodeInventory(ctx context.Context, peer agentwire.Peer, m agentwire.Message) (agentwire.Message, error) {
	var report agentwire.NodeInventory
	if err := agentwire.Decode(m, agentwire.KindNodeInventory, &report); err != nil {
		return agentwire.Message{}, err
	}
	var overview *localinv.Overview
	if len(report.Overview) > localinv.MaxSnapshotBytes || json.Unmarshal(report.Overview, &overview) != nil || overview == nil {
		slog.Debug("Agent inventory rejected", "agent", peer.ID, "bytes", len(report.Overview))
		return agentwire.Encode(agentwire.KindNodeInventoryAck, agentwire.NodeInventoryAck{Rejected: true})
	}
	// Persist only the documented fields of an authenticated node report.
	document, err := json.Marshal(overview)
	if err != nil || len(document) > localinv.MaxSnapshotBytes {
		return agentwire.Encode(agentwire.KindNodeInventoryAck, agentwire.NodeInventoryAck{Rejected: true})
	}

	now := time.Now()
	record := func(store *observability.Store) error {
		writeCtx, cancel := context.WithTimeout(ctx, healthWriteTimeout)
		defer cancel()
		return store.RecordInventory(writeCtx, peer.ID, peer.Generation, document, now)
	}
	live := s.NodeState
	if live == nil {
		// Older configurations use the health store for the live view too.
		live = s.Health
	}
	if live == nil {
		return agentwire.Encode(agentwire.KindNodeInventoryAck, agentwire.NodeInventoryAck{Retry: true})
	}
	if err := record(live); err != nil {
		slog.Debug("Agent inventory write needs retry", "agent", peer.ID, "err", err)
		return agentwire.Encode(agentwire.KindNodeInventoryAck, agentwire.NodeInventoryAck{Retry: true})
	}
	if s.Health != nil && s.Health != live {
		if err := record(s.Health); err != nil {
			slog.Debug("Agent inventory history not retained", "agent", peer.ID, "err", err)
		}
	}
	return agentwire.Encode(agentwire.KindNodeInventoryAck, agentwire.NodeInventoryAck{})
}
