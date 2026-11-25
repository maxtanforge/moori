package ledger

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"time"

	"github.com/maxtanforge/moori/internal/core/deployment"
)

type Event struct {
	ID, Execution string
	Sequence      int64
	State         deployment.State
}

// Record writes the local phase and report together. The executor must
// commit Executing before beginning Docker operations.
func (l *Ledger) Record(ctx context.Context, id string, state deployment.State) error {
	switch state {
	case deployment.Executing, deployment.Verifying, deployment.Succeeded, deployment.Failed, deployment.Canceled, deployment.Reconciling:
	default:
		return ErrState
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := recordTx(ctx, tx, id, state); err != nil {
		return err
	}
	return tx.Commit()
}

func recordTx(ctx context.Context, tx *sql.Tx, id string, state deployment.State) error {
	var from deployment.State
	var sequence int64
	if err := tx.QueryRowContext(ctx, "SELECT state,sequence FROM executions WHERE id=?", id).Scan(&from, &sequence); err != nil {
		return err
	}
	if !deployment.CanTransition(from, state) {
		return ErrState
	}
	sequence++
	if _, err := tx.ExecContext(ctx, "UPDATE executions SET state=?,sequence=? WHERE id=?", state, sequence, id); err != nil {
		return err
	}
	eventID := id + ":" + strconv.FormatInt(sequence, 10)
	if _, err := tx.ExecContext(ctx, "INSERT INTO execution_events(id,execution,sequence,state) VALUES (?,?,?,?)", eventID, id, sequence, state); err != nil {
		return err
	}
	return nil
}

// RetireInterrupted closes local ownership after a trusted offline operator
// has inspected Docker and determined that the execution is no longer active.
func (l *Ledger) RetireInterrupted(ctx context.Context, id, operator, note string) error {
	operator, note = strings.TrimSpace(operator), strings.TrimSpace(note)
	if id == "" || operator == "" || len(operator) > 128 || note == "" || len(note) > 2048 {
		return ErrInvalid
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := recordTx(ctx, tx, id, deployment.Abandoned); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO execution_retirements(execution_id,operator,note)
 VALUES (?,?,?)`, id, operator, note); err != nil {
		return err
	}
	return tx.Commit()
}

// RecoverInterrupted runs before the sole Agent worker starts. An execution
// that crossed the launch gate may have changed Docker before the process died.
func (l *Ledger) RecoverInterrupted(ctx context.Context) error {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	const interrupted = " WHERE state IN (?,?)"
	args := []any{deployment.Reconciling, deployment.Executing, deployment.Verifying}
	if _, err := tx.ExecContext(ctx, `INSERT INTO execution_events(id,execution,sequence,state)
 SELECT id || ':' || (sequence+1),id,sequence+1,? FROM executions`+interrupted+" ORDER BY rowid", args...); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE executions SET state=?,sequence=sequence+1"+interrupted, args...); err != nil {
		return err
	}
	return tx.Commit()
}

func (l *Ledger) Pending(ctx context.Context, limit int) ([]Event, error) {
	if limit <= 0 || limit > 1000 {
		return nil, ErrInvalid
	}
	rows, err := l.db.QueryContext(ctx, `SELECT id,execution,sequence,state FROM execution_events
 WHERE acknowledged=0 ORDER BY ordinal LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.Execution, &e.Sequence, &e.State); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// Acknowledge is safe to repeat and must follow a durable control commit.
func (l *Ledger) Acknowledge(ctx context.Context, id string) error {
	if id == "" {
		return ErrInvalid
	}
	_, err := l.db.ExecContext(ctx, "UPDATE execution_events SET acknowledged=1 WHERE id=?", id)
	return err
}

// AcknowledgeAll marks a batch of events acknowledged in one transaction, so
// a delivery burst costs one commit instead of one commit per event. Safe to
// repeat and must follow durable control commits.
func (l *Ledger) AcknowledgeAll(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		if id == "" {
			return ErrInvalid
		}
		if _, err := tx.ExecContext(ctx, "UPDATE execution_events SET acknowledged=1 WHERE id=?", id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Prune advances a persistent expiration floor so clock rollback cannot make
// an expired request admissible after its local record has been deleted.
func (l *Ledger) Prune(ctx context.Context, cutoff time.Time) error {
	if cutoff.IsZero() {
		return ErrInvalid
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "UPDATE retention SET cutoff_ms=max(cutoff_ms,?) WHERE singleton=1", cutoff.UnixMilli()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM executions
 WHERE expires_ms <= (SELECT cutoff_ms FROM retention WHERE singleton=1)
 AND state IN ('succeeded','failed','canceled','abandoned')
 AND NOT EXISTS (SELECT 1 FROM execution_events e WHERE e.execution=executions.id AND e.acknowledged=0)`); err != nil {
		return err
	}
	return tx.Commit()
}

// Active lists work that requires inspection before the Agent can resume.
func (l *Ledger) Active(ctx context.Context, after string, limit int) ([]string, error) {
	if limit <= 0 || limit > 1000 {
		return nil, ErrInvalid
	}
	rows, err := l.db.QueryContext(ctx, `SELECT id FROM executions
 WHERE id>? AND state NOT IN ('succeeded','failed','canceled','abandoned') ORDER BY id LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
