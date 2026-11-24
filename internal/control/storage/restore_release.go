package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/maxtanforge/moori/internal/core/deployment"
)

const releasedTarget = `EXISTS (
 SELECT 1 FROM recovery_target_releases rr
 JOIN target_assignments ta ON ta.target=rr.target AND ta.agent=rr.agent AND ta.generation=rr.generation
 JOIN agent_certificates ac ON ac.agent=ta.agent AND ac.generation=ta.generation
 WHERE rr.epoch=rs.epoch AND rr.target=d.target)`

// A release is an operator attestation for this epoch. Subsequent credential
// rotation fences the affected placement without reopening global recovery.
const needsRecoveryRelease = `t.restore_epoch=rs.epoch AND NOT EXISTS (
 SELECT 1 FROM recovery_target_releases r WHERE r.epoch=t.restore_epoch AND r.target=t.id
 )`

const recoveryAwaitingRelease = `EXISTS (
 SELECT 1 FROM recovery_state rs JOIN targets t ON ` + needsRecoveryRelease + `
 )`

const canDispatchTarget = `NOT EXISTS (SELECT 1 FROM recovery_state rs WHERE NOT ` + releasedTarget + `
 AND (EXISTS (SELECT 1 FROM targets rt WHERE rt.id=d.target AND rt.restore_epoch=rs.epoch)
    OR ` + recoveryAwaitingRelease + `))`

func requireRecoveryComplete(ctx context.Context, tx *sql.Tx) error {
	var pending bool
	if err := tx.QueryRowContext(ctx, "SELECT "+recoveryAwaitingRelease).Scan(&pending); err != nil {
		return err
	}
	if pending {
		return ErrRecovery
	}
	return nil
}

// releasedTargetForAdministration retains the recovery fence per target, while
// allowing replacement after certificate rotation invalidates the old identity.
// The assignment must still match the release; a mere historical release is not
// sufficient. The epoch remains available to classify delayed reports.
func releasedTargetForAdministration(ctx context.Context, tx *sql.Tx, target string) (string, error) {
	epoch, err := recoveryEpoch(ctx, tx)
	if err != nil || epoch == "" {
		return epoch, err
	}
	var restored bool
	if err := tx.QueryRowContext(ctx, "SELECT restore_epoch=? FROM targets WHERE id=?", epoch, target).Scan(&restored); err != nil {
		return "", err
	}
	if !restored {
		return "", requireRecoveryComplete(ctx, tx)
	}
	var agent string
	var generation int64
	err = tx.QueryRowContext(ctx, `SELECT rr.agent,rr.generation FROM recovery_target_releases rr
 JOIN target_assignments ta ON ta.target=rr.target AND ta.agent=rr.agent AND ta.generation=rr.generation
 WHERE rr.epoch=? AND rr.target=?`, epoch, target).Scan(&agent, &generation)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrRecovery
	}
	if err != nil {
		return "", err
	}
	return epoch, checkRecoveryActivity(ctx, tx, epoch, target, agent)
}

func targetDispatchEpoch(ctx context.Context, tx *sql.Tx, target string) (string, error) {
	epoch, err := recoveryEpoch(ctx, tx)
	if err != nil || epoch == "" {
		return epoch, err
	}
	var ready bool
	err = tx.QueryRowContext(ctx, `SELECT `+canDispatchTarget+` FROM (SELECT id AS target FROM targets WHERE id=?) d`, target).Scan(&ready)
	if err != nil {
		return "", err
	}
	if !ready {
		return "", ErrRecovery
	}
	return epoch, nil
}

// ReleaseRestoredTarget applies the operator's external inspection after relevant
// evidence is quiet. A rotated Agent must inventory at its current generation.
func (s *Store) ReleaseRestoredTarget(ctx context.Context, epoch, project, target, operator, note string) error {
	note = strings.TrimSpace(note)
	operator = strings.TrimSpace(operator)
	if epoch == "" || project == "" || target == "" || operator == "" || len(operator) > 128 || note == "" || len(note) > 2048 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := recoveryEpoch(ctx, tx)
	if err != nil {
		return err
	}
	if epoch != current {
		return ErrConflict
	}
	var agent string
	var originalGeneration, generation int64
	// A locked execution also supplies the original placement for databases that
	// claimed work through the low-level API before recording an assignment.
	err = tx.QueryRowContext(ctx, `SELECT ac.agent,coalesce(a.generation,d.generation),ac.generation FROM targets t
 LEFT JOIN target_assignments a ON a.target=t.id
 LEFT JOIN target_slots s ON s.target=t.id
 LEFT JOIN deployments d ON d.id=s.deployment_id
 JOIN agent_certificates ac ON ac.agent=coalesce(a.agent,d.agent) AND ac.generation>=coalesce(a.generation,d.generation)
 WHERE t.id=? AND t.project=? AND t.restore_epoch=?`, target, project, epoch).
		Scan(&agent, &originalGeneration, &generation)
	if err != nil {
		return err
	}
	var previousOperator, previousNote string
	err = tx.QueryRowContext(ctx, "SELECT operator,note FROM recovery_target_releases WHERE epoch=? AND target=?", epoch, target).
		Scan(&previousOperator, &previousNote)
	if err == nil {
		if previousOperator != operator || previousNote != note {
			return ErrConflict
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err := checkRecoveryEvidence(ctx, tx, epoch, target, agent, generation); err != nil {
		return err
	}
	if err := archiveRestoredSlot(ctx, tx, target, agent, originalGeneration); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO target_assignments(target,agent,generation) VALUES (?,?,?)
 ON CONFLICT(target) DO UPDATE SET generation=excluded.generation`, target, agent, generation); err != nil {
		return err
	}
	if originalGeneration != generation {
		detail := fmt.Sprintf("%s generation %d to %d", agent, originalGeneration, generation)
		if err := addProjectAudit(ctx, tx, project, "target.recovery_rebound", "local:"+operator, target, detail); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE targets SET last_success='' WHERE id=?", target); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO recovery_target_releases(epoch,target,agent,generation,operator,note)
 VALUES (?,?,?,?,?,?)`, epoch, target, agent, generation, operator, note); err != nil {
		return err
	}
	return tx.Commit()
}

func checkRecoveryEvidence(ctx context.Context, tx *sql.Tx, epoch, target, agent string, generation int64) error {
	var complete bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM recovery_inventory_complete
 WHERE epoch=? AND agent=? AND generation=?)`, epoch, agent, generation).Scan(&complete); err != nil {
		return err
	}
	if !complete {
		return fmt.Errorf("%w: current Agent has not completed inventory", ErrRecovery)
	}
	return checkRecoveryActivity(ctx, tx, epoch, target, agent)
}

const recoveryObservations = `WITH observed AS (
 SELECT i.epoch,i.execution_id,i.agent,i.generation,i.target,i.sequence,i.state
 FROM recovery_inventory i JOIN recovery_state rs ON rs.epoch=i.epoch
 UNION ALL
 SELECT r.epoch,r.execution_id,r.agent,r.generation,coalesce(i.target,d.target,''),r.sequence,r.state
 FROM recovery_reports r JOIN recovery_state rs ON rs.epoch=r.epoch
 LEFT JOIN recovery_inventory i ON i.epoch=r.epoch AND i.agent=r.agent AND i.generation=r.generation AND i.execution_id=r.execution_id
 LEFT JOIN deployments d ON d.execution_id=r.execution_id AND d.agent=r.agent AND d.generation=r.generation
 )`

// Only a later observation from the same execution and generation can settle
// old evidence. Certificate rotation alone says nothing about Docker activity.
const latestActiveRecoveryObservation = `o.state NOT IN ('succeeded','failed','canceled','abandoned')
 AND NOT EXISTS (SELECT 1 FROM observed n WHERE n.epoch=o.epoch AND n.agent=o.agent AND n.generation=o.generation
  AND n.execution_id=o.execution_id AND n.sequence>o.sequence)`

func checkRecoveryActivity(ctx context.Context, tx *sql.Tx, epoch, target, agent string) error {
	var busy bool
	// Attribute an unrecognized target conservatively to every target on that
	// Agent. A report with a recognized target stays scoped to that target.
	if err := tx.QueryRowContext(ctx, recoveryObservations+`
 SELECT EXISTS(SELECT 1 FROM observed o
 WHERE o.epoch=? AND (o.target=? OR (o.target='' AND o.agent=?))
 AND `+latestActiveRecoveryObservation+`)`, epoch, target, agent).Scan(&busy); err != nil {
		return err
	}
	if busy {
		return fmt.Errorf("%w: Agent evidence may still show active execution", ErrTargetBusy)
	}
	return nil
}

func archiveRestoredSlot(ctx context.Context, tx *sql.Tx, target, agent string, generation int64) error {
	var id, oldAgent string
	var oldGeneration, expires int64
	var state deployment.State
	err := tx.QueryRowContext(ctx, `SELECT d.id,d.state,d.agent,d.generation,coalesce(d.expires_ms,0) FROM target_slots s
 JOIN deployments d ON d.id=s.deployment_id WHERE s.target=?`, target).Scan(&id, &state, &oldAgent, &oldGeneration, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if oldAgent != agent || oldGeneration != generation || !deployment.CanTransition(state, deployment.Abandoned) {
		return ErrConflict
	}
	if expires > time.Now().UnixMilli() {
		return fmt.Errorf("%w: previous execution offer has not expired", ErrTargetBusy)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE deployments SET state=? WHERE id=?", deployment.Abandoned, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO deployment_audit(deployment_id,state,reason)
 VALUES (?,?,'historical attempt closed after operator Docker inspection')`, id, deployment.Abandoned); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "DELETE FROM target_slots WHERE target=?", target)
	return err
}
