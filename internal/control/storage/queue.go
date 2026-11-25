package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/maxtanforge/moori/internal/core/deployment"
)

// RegisterAssignedTarget provisions a new target only if the Agent generation
// exists. The target and its node assignment commit together.
func (s *Store) RegisterAssignedTarget(ctx context.Context, project, target, agent string, generation int64) error {
	if project == "" || target == "" || agent == "" || generation <= 0 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := requireRecoveryComplete(ctx, tx); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO targets(id,project)
 SELECT ?,? WHERE EXISTS (SELECT 1 FROM agent_certificates WHERE agent=? AND generation=?)`,
		target, project, agent, generation)
	if err := expectOne(res, err, ErrConflict); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO target_assignments(target,agent,generation) VALUES (?,?,?)", target, agent, generation); err != nil {
		return err
	}
	return tx.Commit()
}

// AssignTarget binds the target to an enrolled Agent generation. Changing an
// assignment while its target slot is occupied would break reconciliation.
func (s *Store) AssignTarget(ctx context.Context, target, agent string, generation int64) error {
	if target == "" || agent == "" || generation <= 0 {
		return ErrInvalid
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO target_assignments(target,agent,generation)
 SELECT ?,?,? WHERE NOT EXISTS (SELECT 1 FROM target_slots WHERE target=?)
 AND NOT `+recoveryAwaitingRelease+`
 AND NOT EXISTS (SELECT 1 FROM recovery_state rs JOIN targets t ON t.restore_epoch=rs.epoch WHERE t.id=?)
 ON CONFLICT(target) DO UPDATE SET agent=excluded.agent,generation=excluded.generation WHERE `+canReassignExternalTarget,
		target, agent, generation, target, target)
	return expectOne(res, err, ErrTargetBusy)
}

// A node identity in the accepted snapshot is immutable through dispatch.
const canReassignExternalTarget = `(target_assignments.agent=excluded.agent AND target_assignments.generation=excluded.generation)
 OR NOT EXISTS (SELECT 1 FROM deployments d JOIN snapshots p ON p.deployment_id=d.id
 WHERE d.target=excluded.target AND d.state='queued'
 AND json_type(p.document,'$.external_node')='object')`

// The actual resource declarations also identify pre-binding snapshots. Queued
// work from those snapshots must fail closed because its intended node is unknown.
const externalResourcesInSnapshot = `(
 coalesce(json_type(p.document,'$.external_node'),'')='object' OR
 EXISTS (SELECT 1 FROM json_each(p.document,'$.compose.volumes') WHERE json_extract(value,'$.external')=1)
 OR EXISTS (SELECT 1 FROM json_each(p.document,'$.compose.networks') WHERE json_extract(value,'$.external')=1)
 )`

const externalAssignmentMatches = `EXISTS (SELECT 1 FROM target_assignments actual
 JOIN agent_certificates c ON c.agent=actual.agent AND c.generation=actual.generation
 WHERE actual.target=d.target AND actual.agent=json_extract(p.document,'$.external_node.agent')
 AND actual.generation=json_extract(p.document,'$.external_node.generation'))`

const invalidExternalAssignment = externalResourcesInSnapshot + " AND NOT " + externalAssignmentMatches

// NextOffer reuses an unacknowledged offer before claiming new work. Claiming,
// target locking and expiry handling run inside one immediate transaction.
func (s *Store) NextOffer(ctx context.Context, agent string, generation int64, now time.Time, ttl time.Duration) (Offer, error) {
	if agent == "" || generation <= 0 || now.IsZero() || ttl < time.Second {
		return Offer{}, ErrInvalid
	}
	// A negative read can delay newly queued work until the next poll. All
	// authorization, claiming and expiry decisions still occur in the transaction.
	var candidate bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM deployments d
 WHERE (d.agent=? AND d.generation=? AND d.state='dispatching') OR
 (d.state='queued' AND EXISTS(SELECT 1 FROM target_assignments a WHERE a.target=d.target AND a.agent=? AND a.generation=?)))`, agent, generation, agent, generation).Scan(&candidate); err != nil {
		return Offer{}, err
	}
	if !candidate {
		return Offer{}, sql.ErrNoRows
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Offer{}, err
	}
	defer tx.Rollback()
	running, err := nodeMutationInProgress(ctx, tx, agent, now)
	if err != nil {
		return Offer{}, err
	}
	if running {
		return Offer{}, sql.ErrNoRows
	}
	if err := expireOffers(ctx, tx, agent, generation, now); err != nil {
		return Offer{}, err
	}
	if err := rejectUnauthorizedQueued(ctx, tx, agent, generation, now); err != nil {
		return Offer{}, err
	}
	if err := rejectStaleExternalQueued(ctx, tx, agent, generation); err != nil {
		return Offer{}, err
	}
	offer, err := scanOffer(tx.QueryRowContext(ctx, offerQuery+`
 WHERE d.agent=? AND d.generation=? AND d.state=? AND d.agent_accepted=0
 AND NOT EXISTS (SELECT 1 FROM deployment_cancellations c WHERE c.deployment_id=d.id)
 AND NOT (`+invalidExternalAssignment+`)
 AND `+canDispatchTarget+`
 AND d.dispatch_epoch=coalesce((SELECT epoch FROM recovery_state WHERE singleton=1),'')
 ORDER BY d.rowid LIMIT 1`,
		agent, generation, deployment.Dispatching))
	if errors.Is(err, sql.ErrNoRows) {
		offer, err = claimQueued(ctx, tx, agent, generation, now, now.Add(ttl))
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Offer{}, err
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return Offer{}, commitErr
	}
	return offer, err
}

func claimQueued(ctx context.Context, tx *sql.Tx, agent string, generation int64, now, expires time.Time) (Offer, error) {
	var id, target string
	err := tx.QueryRowContext(ctx, `SELECT d.id,d.target FROM deployments d
 JOIN target_assignments a ON a.target=d.target JOIN snapshots p ON p.deployment_id=d.id
 WHERE a.agent=? AND a.generation=? AND d.state=?
 AND `+canDispatchTarget+`
 AND `+credentialActive+`
 AND NOT (`+invalidExternalAssignment+`)
 AND NOT EXISTS (SELECT 1 FROM target_slots slot WHERE slot.target=d.target)
 ORDER BY d.rowid LIMIT 1`, agent, generation, deployment.Queued, now.UnixMilli()).Scan(&id, &target)
	if err != nil {
		return Offer{}, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO target_slots(target,deployment_id) VALUES (?,?)", target, id); err != nil {
		return Offer{}, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE deployments SET state=?,execution_id=?,agent=?,generation=?,expires_ms=?,
 dispatch_epoch=coalesce((SELECT epoch FROM recovery_state WHERE singleton=1),'')
 WHERE id=? AND state=?`, deployment.Dispatching, id, agent, generation, expires.UnixMilli(), id, deployment.Queued)
	if err := expectOne(res, err, ErrState); err != nil {
		return Offer{}, err
	}
	if err := audit(ctx, tx, id, deployment.Dispatching); err != nil {
		return Offer{}, err
	}
	return scanOffer(tx.QueryRowContext(ctx, offerQuery+" WHERE d.id=?", id))
}

func expireOffers(ctx context.Context, tx *sql.Tx, agent string, generation int64, now time.Time) error {
	const expired = ` WHERE agent=? AND generation=? AND state=? AND
 (expires_ms<=? OR (agent_accepted=0 AND NOT ` + credentialActive + `))`
	args := []any{deployment.Reconciling, agent, generation, deployment.Dispatching, now.UnixMilli(), now.UnixMilli()}
	if _, err := tx.ExecContext(ctx, "INSERT INTO deployment_audit(deployment_id,state) SELECT id,? FROM deployments d"+expired, args...); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "UPDATE deployments AS d SET state=?"+expired, args...)
	return err
}

func rejectUnauthorizedQueued(ctx context.Context, tx *sql.Tx, agent string, generation int64, now time.Time) error {
	const unauthorized = ` WHERE d.state=? AND NOT ` + credentialActive + ` AND EXISTS (
 SELECT 1 FROM target_assignments a WHERE a.target=d.target AND a.agent=? AND a.generation=?)`
	args := []any{deployment.Failed, deployment.Queued, now.UnixMilli(), agent, generation}
	if _, err := tx.ExecContext(ctx, `INSERT INTO deployment_audit(deployment_id,state,reason)
 SELECT id,?,CASE WHEN human_submitter IS NULL THEN 'service credential expired or revoked' ELSE 'project deploy grant withdrawn' END
 FROM deployments d`+unauthorized, args...); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "UPDATE deployments AS d SET state=?"+unauthorized, args...)
	return err
}

func rejectStaleExternalQueued(ctx context.Context, tx *sql.Tx, agent string, generation int64) error {
	const stale = ` WHERE d.state=? AND ` + canDispatchTarget + ` AND EXISTS(
 SELECT 1 FROM snapshots p JOIN target_assignments a ON a.target=d.target
 WHERE p.deployment_id=d.id AND a.agent=? AND a.generation=? AND ` + invalidExternalAssignment + `)`
	args := []any{deployment.Failed, deployment.Queued, agent, generation}
	if _, err := tx.ExecContext(ctx, `INSERT INTO deployment_audit(deployment_id,state,reason)
 SELECT id,?,'external resources require the recorded Agent assignment' FROM deployments d`+stale, args...); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "UPDATE deployments AS d SET state=?"+stale, args...)
	return err
}

// AcknowledgeOffer tracks the admission receipt independently from ordered
// execution events. A late receipt does not change deployment state.
func (s *Store) AcknowledgeOffer(ctx context.Context, execution, agent string, generation int64) error {
	if execution == "" || agent == "" || generation <= 0 {
		return ErrInvalid
	}
	res, err := s.db.ExecContext(ctx, `UPDATE deployments SET agent_accepted=1
 WHERE execution_id=? AND agent=? AND generation=?`, execution, agent, generation)
	return expectOne(res, err, ErrConflict)
}
