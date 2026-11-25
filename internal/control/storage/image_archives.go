package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/maxtanforge/moori/internal/core/nodeops"
)

// HumanImageTransfer permits the initiating platform administrator to attach
// to a live archive operation, even while it is waiting for its node.
func (s *Store) HumanImageTransfer(ctx context.Context, token, agent, id string, now time.Time) (nodeops.Action, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	actor, err := platformActor(ctx, tx, token)
	if err != nil {
		return "", err
	}
	var action nodeops.Action
	err = tx.QueryRowContext(ctx, `SELECT q.action FROM node_operations q
 JOIN agent_certificates a ON a.agent=q.agent AND a.generation=q.generation
 WHERE q.agent=? AND q.id=? AND q.actor=? AND q.state IN ('queued','running') AND q.expires_ms>?
 AND q.action IN ('image.export','image.import') AND NOT EXISTS (
 SELECT 1 FROM agent_credentials c WHERE c.agent=q.agent AND c.generation=q.generation AND (c.revoked=1 OR c.expires_ms<=?))`,
		agent, id, actor, now.UnixMilli(), now.UnixMilli()).Scan(&action)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrAccessDenied
	}
	if err != nil {
		return "", err
	}
	return action, tx.Commit()
}
