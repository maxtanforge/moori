package dispatch

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/maxtanforge/moori/internal/core/nodeops"
	"github.com/maxtanforge/moori/internal/transport/agentwire"
)

func (s Session) operationPoll(ctx context.Context, peer agentwire.Peer, m agentwire.Message) (agentwire.Message, error) {
	if err := agentwire.Decode(m, m.Kind, &struct{}{}); err != nil {
		return agentwire.Message{}, err
	}
	now := time.Now()
	if err := s.Store.RecordNodeOperationCapability(ctx, peer.ID, peer.Generation, now); err != nil {
		return agentwire.Message{}, err
	}
	if command, pending, err := s.nextCancellation(ctx, peer); pending || err != nil {
		return command, err
	}
	if m.Kind == agentwire.KindNodeOperationHeartbeat {
		return s.idleOrRecovery(ctx)
	}
	req, expires, err := s.Store.NextNodeOperation(ctx, peer.ID, peer.Generation, now)
	if errors.Is(err, sql.ErrNoRows) {
		return s.pollDeployment(ctx, peer, now)
	}
	if err != nil {
		return agentwire.Message{}, err
	}
	return agentwire.Encode(agentwire.KindNodeOperation, agentwire.NodeOperation{ID: req.Operation, ExpiresMS: expires.UnixMilli(), Request: req})
}

func (s Session) operationResult(ctx context.Context, peer agentwire.Peer, m agentwire.Message) (agentwire.Message, error) {
	var report agentwire.NodeOperationResult
	if err := agentwire.Decode(m, agentwire.KindNodeOperationResult, &report); err != nil {
		return agentwire.Message{}, err
	}
	// A pruned operation or an operation missing from a restored database
	// cannot accept a result. Acknowledge it so the node can keep polling.
	err := s.Store.CompleteNodeOperation(ctx, peer.ID, peer.Generation, report.ID, report.State, report.Error, report.Result, time.Now())
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return agentwire.Message{}, err
	}
	if err == nil && report.State != nodeops.Succeeded && s.AbortArchive != nil {
		s.AbortArchive(report.ID)
	}
	return agentwire.Encode(agentwire.KindNodeOperationResultAck, agentwire.Ack{ID: report.ID})
}
