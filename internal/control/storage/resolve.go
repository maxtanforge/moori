package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/maxtanforge/moori/internal/core/deployment"
)

// AbandonFencedDeployment is a trusted local operation. The operator must have
// stopped/fenced Docker on the old host, not merely disconnected its network.
// A newer certificate generation additionally prevents old peers reconnecting.
func (s *Store) AbandonFencedDeployment(ctx context.Context, id, operator, note string, hostFenced bool) error {
	operator, note = strings.TrimSpace(operator), strings.TrimSpace(note)
	if !hostFenced || id == "" || operator == "" || len(operator) > 128 || note == "" || len(note) > 2048 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var project, target, agent string
	var generation, expires int64
	var state deployment.State
	err = tx.QueryRowContext(ctx, `SELECT project,target,coalesce(agent,''),coalesce(generation,0),coalesce(expires_ms,0),state FROM deployments WHERE id=?`, id).Scan(&project, &target, &agent, &generation, &expires, &state)
	if err != nil {
		return err
	}
	if _, err := releasedTargetForAdministration(ctx, tx, target); err != nil {
		return err
	}
	if state == deployment.Abandoned {
		var actor, detail string
		err := tx.QueryRowContext(ctx, `SELECT actor,detail FROM project_audit WHERE subject=? AND action='deployment.abandoned' ORDER BY id DESC LIMIT 1`, id).Scan(&actor, &detail)
		if err != nil || actor != "local:"+operator || detail != note {
			return ErrConflict
		}
		return tx.Commit()
	}
	if state != deployment.Dispatching && state != deployment.Executing && state != deployment.Verifying && state != deployment.Reconciling && state != deployment.NeedsAttention {
		return ErrState
	}
	if expires == 0 || expires > time.Now().UnixMilli() {
		return ErrTargetBusy
	}
	var fenced bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_certificates WHERE agent=? AND generation>?)`, agent, generation).Scan(&fenced); err != nil {
		return err
	}
	if !fenced {
		return ErrForbidden
	}
	if state != deployment.Reconciling && state != deployment.NeedsAttention {
		if err := changeState(ctx, tx, id, state, deployment.Reconciling); err != nil {
			return err
		}
		state = deployment.Reconciling
	}
	if err := changeState(ctx, tx, id, state, deployment.Abandoned); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM target_slots WHERE target=? AND deployment_id=?`, target, id)
	if err := expectOne(res, err, ErrConflict); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE targets SET last_success='' WHERE id=?`, target); err != nil {
		return err
	}
	if err := addProjectAudit(ctx, tx, project, "deployment.abandoned", "local:"+operator, id, note); err != nil {
		return err
	}
	return tx.Commit()
}

// ReassignTarget checks the current registered identity in the same transaction
// as the empty-slot guard. Unreleased recovery assignments remain immutable.
func (s *Store) ReassignTarget(ctx context.Context, project, target, agent string, generation int64) error {
	if project == "" || target == "" || agent == "" || generation <= 0 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current string
	err = tx.QueryRowContext(ctx, `SELECT id FROM targets WHERE id=? AND project=? AND EXISTS(SELECT 1 FROM agent_certificates WHERE agent=? AND generation=?)`, target, project, agent, generation).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	epoch, err := releasedTargetForAdministration(ctx, tx, target)
	if errors.Is(err, ErrRecovery) {
		return ErrTargetBusy
	}
	if err != nil {
		return err
	}
	if epoch != "" {
		// Unknown evidence from the replacement identity must not be bypassed.
		if err := checkRecoveryActivity(ctx, tx, epoch, target, agent); err != nil {
			return err
		}
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO target_assignments(target,agent,generation) SELECT ?,?,?
 WHERE NOT EXISTS(SELECT 1 FROM target_slots WHERE target=?)
 ON CONFLICT(target) DO UPDATE SET agent=excluded.agent,generation=excluded.generation WHERE `+canReassignExternalTarget, target, agent, generation, target)
	if err := expectOne(res, err, ErrTargetBusy); err != nil {
		return err
	}
	if epoch != "" {
		// Carry forward the released target's status atomically with its new
		// assignment, so dispatch does not become stuck on the retired identity.
		res, err := tx.ExecContext(ctx, `UPDATE recovery_target_releases SET agent=?,generation=? WHERE epoch=? AND target=?`, agent, generation, epoch, target)
		if err := expectOne(res, err, ErrConflict); err != nil {
			return err
		}
	}
	if err := addProjectAudit(ctx, tx, project, "target.reassigned", "local", target, agent); err != nil {
		return err
	}
	return tx.Commit()
}
