package storage

import (
	"context"
	"database/sql"
	"errors"

	"github.com/maxtanforge/moori/internal/core/deployment"
)

// Event is a state report from an authenticated Agent. The caller verifies the
// current connection identity and execution evidence before applying it.
type Event struct {
	ID, Execution, Agent string
	Generation, Sequence int64
	State                deployment.State
}

// MarkReconciling retains the target slot while delivery or execution is uncertain.
func (s *Store) MarkReconciling(ctx context.Context, execution string, generation int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id string
	var from deployment.State
	if err := tx.QueryRowContext(ctx, `SELECT id,state FROM deployments WHERE execution_id=? AND generation=?`, execution, generation).Scan(&id, &from); err != nil {
		return err
	}
	if from == deployment.Reconciling {
		return tx.Commit()
	}
	if !deployment.CanTransition(from, deployment.Reconciling) {
		return ErrState
	}
	res, err := tx.ExecContext(ctx, "UPDATE deployments SET state=? WHERE id=? AND state=?", deployment.Reconciling, id, from)
	if err := expectOne(res, err, ErrState); err != nil {
		return err
	}
	if err := audit(ctx, tx, id, deployment.Reconciling); err != nil {
		return err
	}
	return tx.Commit()
}

// ApplyEvent acknowledges identical replays even after a terminal transition.
func (s *Store) ApplyEvent(ctx context.Context, e Event) error {
	if e.ID == "" || e.Execution == "" || e.Agent == "" || e.Generation <= 0 || e.Sequence <= 0 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id, agent, target, release string
	epoch, err := recoveryEpoch(ctx, tx)
	if err != nil {
		return err
	}
	if epoch != "" {
		var fresh bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM deployments WHERE execution_id=? AND dispatch_epoch=?)`,
			e.Execution, epoch).Scan(&fresh); err != nil {
			return err
		}
		if !fresh {
			if err := recordRecoveryReport(ctx, tx, epoch, e); err != nil {
				return err
			}
			return tx.Commit()
		}
	}
	var generation, sequence int64
	var from deployment.State
	err = tx.QueryRowContext(ctx, `SELECT id,agent,generation,sequence,state,target,release_id
 FROM deployments WHERE execution_id=?`, e.Execution).Scan(&id, &agent, &generation, &sequence, &from, &target, &release)
	if err != nil {
		return err
	}
	if agent != e.Agent || generation != e.Generation {
		return ErrConflict
	}

	var oldExecution string
	var oldSequence int64
	var oldState deployment.State
	err = tx.QueryRowContext(ctx, "SELECT execution_id,sequence,state FROM receipts WHERE id=?", e.ID).
		Scan(&oldExecution, &oldSequence, &oldState)
	switch {
	case err == nil:
		if oldExecution != e.Execution || oldSequence != e.Sequence || oldState != e.State {
			return ErrConflict
		}
		return tx.Commit()
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	// Agent reports carry contiguous sequences. A gap is retried after the Agent
	// replays the missing receipts; local control transitions use no sequence.
	sameReconciliation := from == deployment.Reconciling && e.State == deployment.Reconciling
	if e.Sequence <= sequence || e.Sequence-sequence != 1 || !agentState(e.State) ||
		(!sameReconciliation && !deployment.CanTransition(from, e.State)) {
		return ErrState
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO receipts(id,execution_id,sequence,state) VALUES (?,?,?,?)",
		e.ID, e.Execution, e.Sequence, e.State); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, "UPDATE deployments SET state=?,sequence=? WHERE id=? AND state=? AND sequence=?", e.State, e.Sequence, id, from, sequence)
	if err := expectOne(res, err, ErrState); err != nil {
		return err
	}
	if e.State == deployment.Succeeded {
		if _, err := tx.ExecContext(ctx, "UPDATE targets SET last_success=? WHERE id=?", release, target); err != nil {
			return err
		}
	}
	if e.State.Terminal() {
		res, err := tx.ExecContext(ctx, "DELETE FROM target_slots WHERE target=? AND deployment_id=?", target, id)
		if err := expectOne(res, err, ErrState); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO outbox(event_id,deployment_id,state) VALUES (?,?,?)", e.ID, id, e.State); err != nil {
			return err
		}
	}
	if err := audit(ctx, tx, id, e.State); err != nil {
		return err
	}
	return tx.Commit()
}

func agentState(s deployment.State) bool {
	switch s {
	case deployment.Executing, deployment.Verifying, deployment.Succeeded, deployment.Failed, deployment.Canceled, deployment.Reconciling, deployment.Abandoned:
		return true
	default:
		return false
	}
}
