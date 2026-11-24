// Package dispatch binds wire operations to control-plane transactions.
package dispatch

import (
	"context"
	"encoding/json"
	"time"

	"github.com/maxtanforge/moori/internal/control/observability"
	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/core/deployment"
	"github.com/maxtanforge/moori/internal/platform/execlog"
	"github.com/maxtanforge/moori/internal/transport/agentwire"
)

type Session struct {
	AbortArchive func(string)
	Store        *storage.Store
	ValidatePeer func(context.Context, agentwire.Peer) error
	Logs         *execlog.Store
	Health       *observability.Store
	NodeState    *observability.Store
}

func (s Session) Offer(ctx context.Context, peer agentwire.Peer, execution string, now time.Time) (agentwire.Message, error) {
	offer, err := s.Store.Offer(ctx, execution)
	if err != nil {
		return agentwire.Message{}, err
	}
	if offer.Agent != peer.ID || offer.Generation != peer.Generation {
		return agentwire.Message{}, storage.ErrConflict
	}
	if offer.State != deployment.Dispatching || !offer.Expires.After(now) {
		return agentwire.Message{}, storage.ErrState
	}
	return encodeOffer(offer)
}

func encodeOffer(offer storage.Offer) (agentwire.Message, error) {
	return agentwire.Encode(agentwire.KindOffer, agentwire.Offer{
		Execution: offer.Execution, Target: offer.Target, Digest: offer.Digest, ExpiresMS: offer.Expires.UnixMilli(), Snapshot: json.RawMessage(offer.Document),
	})
}

// Report derives identity from the authenticated connection and constructs
// the ACK only after the control-plane transaction commits.
func (s Session) Report(ctx context.Context, peer agentwire.Peer, m agentwire.Message) (agentwire.Message, error) {
	var r agentwire.Report
	if err := agentwire.Decode(m, agentwire.KindReport, &r); err != nil {
		return agentwire.Message{}, err
	}
	err := s.Store.ApplyEvent(ctx, storage.Event{ID: r.ID, Execution: r.Execution, Agent: peer.ID, Generation: peer.Generation, Sequence: r.Sequence, State: r.State})
	if err != nil {
		return agentwire.Message{}, err
	}
	return agentwire.Encode(agentwire.KindAck, agentwire.Ack{ID: r.ID})
}
