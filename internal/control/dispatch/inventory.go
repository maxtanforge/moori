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

// nodeInventory stores the latest machine resource snapshot the Agent pushed
// after connecting. Malformed snapshots are acknowledged but not stored: the
// Agent always sends well-formed JSON it produced itself, and failing the
// channel over a bad snapshot would only loop a reconnect.
func (s Session) nodeInventory(ctx context.Context, peer agentwire.Peer, m agentwire.Message) (agentwire.Message, error) {
	var report agentwire.NodeInventory
	if err := agentwire.Decode(m, agentwire.KindNodeInventory, &report); err != nil {
		return agentwire.Message{}, err
	}
	if s.Health == nil && s.NodeState == nil {
		return agentwire.Encode(agentwire.KindNodeInventoryAck, struct{}{})
	}
	var overview *localinv.Overview
	if len(report.Overview) <= localinv.MaxSnapshotBytes && json.Unmarshal(report.Overview, &overview) == nil && overview != nil {
		// Persist only the documented fields of an authenticated node report.
		document, err := json.Marshal(overview)
		if err != nil || len(document) > localinv.MaxSnapshotBytes {
			return agentwire.Encode(agentwire.KindNodeInventoryAck, struct{}{})
		}
		now := time.Now()
		for _, store := range []*observability.Store{s.NodeState, s.Health} {
			if store == nil {
				continue
			}
			writeCtx, cancel := context.WithTimeout(ctx, healthWriteTimeout)
			if err := store.RecordInventory(writeCtx, peer.ID, peer.Generation, document, now); err != nil {
				slog.Debug("Agent inventory not retained", "agent", peer.ID, "err", err)
			}
			cancel()
		}
	} else {
		slog.Debug("Agent inventory rejected", "agent", peer.ID, "bytes", len(report.Overview))
	}
	return agentwire.Encode(agentwire.KindNodeInventoryAck, struct{}{})
}
