package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/maxtanforge/moori/internal/core/deployment"
)

type DeploymentDetails struct {
	ID             string           `json:"id"`
	State          deployment.State `json:"state"`
	Release        string           `json:"release"`
	Digest         string           `json:"digest"`
	Snapshot       json.RawMessage  `json:"snapshot"`
	RollbackOf     string           `json:"rollback_of,omitempty"`
	RollbackReason string           `json:"rollback_reason,omitempty"`
	Execution      string           `json:"-"`
}

// ReadHumanDeployment exposes the immutable deployment snapshot to project members.
func (s *Store) ReadHumanDeployment(ctx context.Context, token, project, id string) (DeploymentDetails, error) {
	hash, err := humanTokenHash(token)
	if err != nil {
		return DeploymentDetails{}, err
	}
	var item DeploymentDetails
	var document string
	err = s.db.QueryRowContext(ctx, `SELECT d.id,d.state,d.release_id,d.digest,p.document,coalesce(d.rollback_of,''),d.rollback_reason,coalesce(d.execution_id,'') FROM deployments d
 JOIN snapshots p ON p.deployment_id=d.id
 JOIN human_sessions u ON u.token_hash=? AND u.expires_ms>?
 LEFT JOIN project_members m ON m.project=d.project AND m.user_id=u.user_id
 WHERE d.project=? AND d.id=? AND `+projectReadGrant,
		hash[:], time.Now().UnixMilli(), project, id).Scan(&item.ID, &item.State, &item.Release, &item.Digest, &document, &item.RollbackOf, &item.RollbackReason, &item.Execution)
	if err != nil {
		return DeploymentDetails{}, err
	}
	item.Snapshot = json.RawMessage(document)
	return item, nil
}

// CancelBeforeDispatch cancels only the request owner's queued work.
// Once dispatch has claimed a target, a cancellation needs Agent evidence.
func (s *Store) CancelBeforeDispatch(ctx context.Context, project, id, principal string) error {
	if project == "" || id == "" || principal == "" {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner string
	var from deployment.State
	if err := tx.QueryRowContext(ctx, "SELECT caller,state FROM deployments WHERE project=? AND id=?", project, id).Scan(&owner, &from); err != nil {
		return err
	}
	if owner != principal {
		return ErrForbidden
	}
	if from == deployment.Canceled {
		return tx.Commit()
	}
	if deployment.CancellationDecision(from) != deployment.CancelLocally {
		return ErrState
	}
	if err := changeState(ctx, tx, id, from, deployment.Canceled); err != nil {
		return err
	}
	return tx.Commit()
}

func changeState(ctx context.Context, tx *sql.Tx, id string, from, to deployment.State) error {
	res, err := tx.ExecContext(ctx, "UPDATE deployments SET state=? WHERE id=? AND state=?", to, id, from)
	if err := expectOne(res, err, ErrState); err != nil {
		return err
	}
	return audit(ctx, tx, id, to)
}
