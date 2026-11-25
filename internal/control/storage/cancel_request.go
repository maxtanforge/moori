package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/maxtanforge/moori/internal/core/deployment"
)

type Cancellation struct {
	ID       string                    `json:"id"`
	State    deployment.State          `json:"state"`
	Decision deployment.CancelDecision `json:"decision"`
}

func (s *Store) RequestCancel(ctx context.Context, id, operator, note string) (Cancellation, error) {
	operator, note = strings.TrimSpace(operator), strings.TrimSpace(note)
	if operator == "" || note == "" || len(operator) > 128 || len(note) > 2048 {
		return Cancellation{}, ErrInvalid
	}
	return s.requestCancel(ctx, id, operator, note, nil, nil)
}

func (s *Store) RequestServiceCancel(ctx context.Context, token, id string) (Cancellation, error) {
	hash, err := credentialHash(token)
	if err != nil {
		return Cancellation{}, err
	}
	return s.requestCancel(ctx, id, "", "requested by deployment CI credential", &hash, nil)
}

func (s *Store) RequestHumanCancel(ctx context.Context, token, project, id, note string) (Cancellation, error) {
	session, err := newProjectSession(project, token)
	if err != nil {
		return Cancellation{}, err
	}
	note = strings.TrimSpace(note)
	if note == "" || len(note) > 2048 {
		return Cancellation{}, ErrInvalid
	}
	return s.requestCancel(ctx, id, "", note, nil, &session)
}

func (s *Store) requestCancel(ctx context.Context, id, operator, note string, credential *[32]byte, session *projectSession) (Cancellation, error) {
	if id == "" {
		return Cancellation{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Cancellation{}, err
	}
	defer tx.Rollback()
	if credential != nil {
		var owner string
		err := tx.QueryRowContext(ctx, `SELECT c.id FROM service_credentials c JOIN deployments d ON d.credential_id=c.id
 WHERE d.id=? AND c.token_hash=? AND c.revoked=0 AND c.expires_ms>?`, id, credential[:], time.Now().UnixMilli()).Scan(&owner)
		if err != nil {
			return Cancellation{}, err
		}
		operator = "service:" + owner
	}
	result := Cancellation{ID: id}
	var target string
	query := `SELECT target,state FROM deployments WHERE id=?`
	args := []any{id}
	if session != nil {
		operator, err = session.authorize(ctx, tx, true)
		if err != nil {
			return Cancellation{}, err
		}
		query += ` AND project=?`
		args = append(args, session.project)
	}
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&target, &result.State); err != nil {
		return Cancellation{}, err
	}
	result.Decision = deployment.CancellationDecision(result.State)
	if result.Decision == deployment.CancelTooLate {
		return result, tx.Commit()
	}
	if result.Decision == deployment.CancelInvalidState {
		return Cancellation{}, ErrState
	}
	if _, err := targetDispatchEpoch(ctx, tx, target); err != nil {
		return Cancellation{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO deployment_cancellations(deployment_id,operator,note) VALUES (?,?,?)
 ON CONFLICT(deployment_id) DO NOTHING`, id, operator, note); err != nil {
		return Cancellation{}, err
	}
	if result.Decision == deployment.CancelLocally {
		if err := changeState(ctx, tx, id, result.State, deployment.Canceled); err != nil {
			return Cancellation{}, err
		}
		result.State = deployment.Canceled
	}
	if err := tx.Commit(); err != nil {
		return Cancellation{}, err
	}
	return result, nil
}

func (s *Store) NextCancel(ctx context.Context, agent string, generation int64) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT d.execution_id FROM deployment_cancellations c JOIN deployments d ON d.id=c.deployment_id
 WHERE d.agent=? AND d.generation=? AND c.acknowledged=0 AND d.execution_id IS NOT NULL
 AND d.state IN ('dispatching','executing','verifying','reconciling') AND `+canDispatchTarget+`
 AND d.dispatch_epoch=coalesce((SELECT epoch FROM recovery_state WHERE singleton=1),'')
 ORDER BY c.rowid LIMIT 1`, agent, generation).Scan(&id)
	return id, err
}

// AcknowledgeCancel records delivery, not success. When the Agent cannot
// identify a running worker, preserve uncertainty and hold the target slot.
func (s *Store) AcknowledgeCancel(ctx context.Context, execution, agent string, generation int64, uncertain bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id string
	var state deployment.State
	err = tx.QueryRowContext(ctx, `SELECT d.id,d.state FROM deployment_cancellations c JOIN deployments d ON d.id=c.deployment_id
 WHERE d.execution_id=? AND d.agent=? AND d.generation=? AND `+canDispatchTarget+`
 AND d.dispatch_epoch=coalesce((SELECT epoch FROM recovery_state WHERE singleton=1),'')`, execution, agent, generation).Scan(&id, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if uncertain && deployment.CanTransition(state, deployment.Reconciling) && state != deployment.NeedsAttention {
		if err := changeState(ctx, tx, id, state, deployment.Reconciling); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE deployment_cancellations SET acknowledged=1 WHERE deployment_id=?", id); err != nil {
		return err
	}
	return tx.Commit()
}
