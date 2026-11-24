package dispatch

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/maxtanforge/moori/internal/transport/agentwire"
)

// Reply handles a single authenticated request. Each request receives a
// response, so the Agent can correlate acknowledgements without multiplexing.
func (s Session) Reply(ctx context.Context, peer agentwire.Peer, m agentwire.Message) (agentwire.Message, error) {
	if s.ValidatePeer != nil {
		if err := s.ValidatePeer(ctx, peer); err != nil {
			return agentwire.Message{}, err
		}
	}
	switch m.Kind {
	case agentwire.KindHeartbeat:
		if err := agentwire.Decode(m, agentwire.KindHeartbeat, &struct{}{}); err != nil {
			return agentwire.Message{}, err
		}
		if command, pending, err := s.nextCancellation(ctx, peer); pending || err != nil {
			return command, err
		}
		return s.idleOrRecovery(ctx)
	case agentwire.KindPoll:
		if err := agentwire.Decode(m, agentwire.KindPoll, &struct{}{}); err != nil {
			return agentwire.Message{}, err
		}
		if command, pending, err := s.nextCancellation(ctx, peer); pending || err != nil {
			return command, err
		}
		return s.pollDeployment(ctx, peer, time.Now())
	case agentwire.KindNodeOperationPoll, agentwire.KindNodeOperationHeartbeat:
		return s.operationPoll(ctx, peer, m)
	case agentwire.KindNodeOperationResult:
		return s.operationResult(ctx, peer, m)
	case agentwire.KindReceipt:
		var receipt agentwire.Receipt
		if err := agentwire.Decode(m, agentwire.KindReceipt, &receipt); err != nil {
			return agentwire.Message{}, err
		}
		if err := s.Store.AcknowledgeOffer(ctx, receipt.Execution, peer.ID, peer.Generation); err != nil {
			return agentwire.Message{}, err
		}
		return agentwire.Encode(agentwire.KindReceiptAck, agentwire.Ack{ID: receipt.Execution})
	case agentwire.KindReport:
		return s.Report(ctx, peer, m)
	case agentwire.KindOutput:
		return s.output(ctx, peer, m)
	case agentwire.KindHostSample:
		return s.hostSample(ctx, peer, m)
	case agentwire.KindNodeInventory:
		return s.nodeInventory(ctx, peer, m)
	case agentwire.KindCancelReply:
		var result agentwire.CancelReply
		if err := agentwire.Decode(m, agentwire.KindCancelReply, &result); err != nil {
			return agentwire.Message{}, err
		}
		if err := s.Store.AcknowledgeCancel(ctx, result.Execution, peer.ID, peer.Generation, result.Uncertain); err != nil {
			return agentwire.Message{}, err
		}
		return agentwire.Encode(agentwire.KindCancelAck, agentwire.Ack{ID: result.Execution})
	case agentwire.KindInventory:
		var page agentwire.Inventory
		if err := agentwire.Decode(m, agentwire.KindInventory, &page); err != nil {
			return agentwire.Message{}, err
		}
		if err := s.Store.RecordInventory(ctx, page.Epoch, peer.ID, peer.Generation, page.Records, page.Complete); err != nil {
			return agentwire.Message{}, err
		}
		return agentwire.Encode(agentwire.KindInventoryAck, agentwire.Ack{ID: page.Epoch})
	default:
		return agentwire.Message{}, errors.New("unexpected request from Agent")
	}
}

func (s Session) pollDeployment(ctx context.Context, peer agentwire.Peer, now time.Time) (agentwire.Message, error) {
	offer, err := s.Store.NextOffer(ctx, peer.ID, peer.Generation, now, time.Minute)
	if errors.Is(err, sql.ErrNoRows) {
		return s.idleOrRecovery(ctx)
	}
	if err != nil {
		return agentwire.Message{}, err
	}
	return encodeOffer(offer)
}

func (s Session) nextCancellation(ctx context.Context, peer agentwire.Peer) (agentwire.Message, bool, error) {
	id, err := s.Store.NextCancel(ctx, peer.ID, peer.Generation)
	if errors.Is(err, sql.ErrNoRows) {
		return agentwire.Message{}, false, nil
	}
	if err != nil {
		return agentwire.Message{}, false, err
	}
	m, err := agentwire.Encode(agentwire.KindCancel, agentwire.Cancel{Execution: id})
	return m, true, err
}

func (s Session) idleOrRecovery(ctx context.Context) (agentwire.Message, error) {
	epoch, err := s.Store.RecoveryEpoch(ctx)
	if err != nil {
		return agentwire.Message{}, err
	}
	if epoch != "" {
		return agentwire.Encode(agentwire.KindRecovery, agentwire.Recovery{Epoch: epoch})
	}
	return agentwire.Encode(agentwire.KindIdle, struct{}{})
}

func (s Session) Serve(ctx context.Context, peer agentwire.Peer, c *agentwire.Channel) error {
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	var lastContact time.Time
	for ctx.Err() == nil {
		m, err := c.Receive()
		if err != nil {
			return err
		}
		reply, err := s.Reply(ctx, peer, m)
		if err != nil {
			return err
		}
		if err := c.Send(reply); err != nil {
			return err
		}
		if now := time.Now(); now.Sub(lastContact) >= 15*time.Second {
			lastContact = now
			s.RecordContact(ctx, peer, now)
		}
	}
	return ctx.Err()
}
