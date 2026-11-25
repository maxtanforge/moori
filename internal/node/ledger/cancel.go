package ledger

import (
	"context"

	"github.com/maxtanforge/moori/internal/core/deployment"
)

// CancelAccepted closes a known execution only if it has not reached its
// durable Docker launch gate. It returns the current state otherwise.
func (l *Ledger) CancelAccepted(ctx context.Context, id string) (deployment.State, error) {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var state deployment.State
	if err := tx.QueryRowContext(ctx, "SELECT state FROM executions WHERE id=?", id).Scan(&state); err != nil {
		return "", err
	}
	if state == deployment.Dispatching {
		if err := recordTx(ctx, tx, id, deployment.Canceled); err != nil {
			return "", err
		}
		state = deployment.Canceled
	}
	return state, tx.Commit()
}
