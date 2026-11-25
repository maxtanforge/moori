// Package session converts Agent wire messages into local ledger operations.
package session

import (
	"context"
	"errors"
	"time"

	"github.com/maxtanforge/moori/internal/core/release"
	"github.com/maxtanforge/moori/internal/node/ledger"
	"github.com/maxtanforge/moori/internal/transport/agentwire"
)

type Session struct {
	Ledger *ledger.Ledger
}

type Admission struct {
	Execution, Document string
	Start               bool
	Receipt             agentwire.Message
}

// Accept validates the received snapshot before allocating a target slot.
// Only a newly committed admission grants Start to the caller.
func (s Session) Accept(ctx context.Context, m agentwire.Message, now time.Time) (Admission, error) {
	var offer agentwire.Offer
	if err := agentwire.Decode(m, agentwire.KindOffer, &offer); err != nil {
		return Admission{}, err
	}
	document := string(offer.Snapshot)
	v, err := release.Verify(ctx, document, offer.Digest)
	if err != nil {
		return Admission{}, err
	}
	if v.Target != offer.Target {
		return Admission{}, ledger.ErrConflict
	}
	start, err := s.Ledger.Accept(ctx, ledger.Request{
		Execution: offer.Execution, Target: offer.Target, Digest: offer.Digest, Document: document, Expires: time.UnixMilli(offer.ExpiresMS),
	}, now)
	if err != nil {
		return Admission{}, err
	}
	entry, err := s.Ledger.Get(ctx, offer.Execution)
	if err != nil {
		return Admission{}, err
	}
	receipt, err := agentwire.Encode(agentwire.KindReceipt, agentwire.Receipt{Execution: offer.Execution, State: entry.State})
	if err != nil {
		return Admission{}, err
	}
	return Admission{Execution: offer.Execution, Document: document, Start: start, Receipt: receipt}, nil
}

func Report(event ledger.Event) (agentwire.Message, error) {
	return agentwire.Encode(agentwire.KindReport, agentwire.Report{
		ID: event.ID, Execution: event.Execution, Sequence: event.Sequence, State: event.State,
	})
}

func (s Session) Acknowledge(ctx context.Context, event ledger.Event, m agentwire.Message) error {
	return s.AcknowledgeAll(ctx, []ledger.Event{event}, []agentwire.Message{m})
}

// AcknowledgeAll validates each acknowledgement against its report, then
// persists the whole batch in one ledger transaction.
func (s Session) AcknowledgeAll(ctx context.Context, events []ledger.Event, acks []agentwire.Message) error {
	if len(events) != len(acks) {
		return errors.New("acknowledgement count does not match the report batch")
	}
	ids := make([]string, 0, len(events))
	for i, event := range events {
		var ack agentwire.Ack
		if err := agentwire.Decode(acks[i], agentwire.KindAck, &ack); err != nil {
			return err
		}
		if ack.ID != event.ID {
			return errors.New("acknowledgement does not match the report")
		}
		ids = append(ids, event.ID)
	}
	return s.Ledger.AcknowledgeAll(ctx, ids)
}
