package ledger

import (
	"context"

	"github.com/maxtanforge/moori/internal/core/deployment"
)

// Inventory includes recently retained terminal rows even if their events
// were acknowledged before the control database was restored.
func (l *Ledger) Inventory(ctx context.Context, after string) ([]deployment.Evidence, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT id,target,digest,sequence,state FROM executions
 WHERE id>? ORDER BY id LIMIT ?`, after, deployment.InventoryPageSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []deployment.Evidence
	for rows.Next() {
		var item deployment.Evidence
		if err := rows.Scan(&item.Execution, &item.Target, &item.Digest, &item.Sequence, &item.State); err != nil {
			return nil, err
		}
		entries = append(entries, item)
	}
	return entries, rows.Err()
}
