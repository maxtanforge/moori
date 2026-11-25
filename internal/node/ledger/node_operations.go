package ledger

import (
	"context"
	"encoding/json"

	"github.com/maxtanforge/moori/internal/core/nodeops"
)

// BeginNodeOperation journals acceptance before invoking Docker. An existing
// receipt is never permission to repeat a mutation.
func (l *Ledger) BeginNodeOperation(ctx context.Context, id string) (bool, error) {
	res, err := l.db.ExecContext(ctx, "INSERT INTO node_operation_receipts(id) VALUES (?) ON CONFLICT DO NOTHING", id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (l *Ledger) FinishNodeOperation(ctx context.Context, receipt nodeops.Operation) error {
	var document []byte
	if receipt.Result != nil {
		result := *receipt.Result
		result.Log = ""
		result.EditConfig = nil
		if result.ComposeSource != nil {
			source := *result.ComposeSource
			source.Files = nil
			result.ComposeSource = &source
		}
		var err error
		document, err = json.Marshal(result)
		if err != nil {
			return err
		}
	}
	_, err := l.db.ExecContext(ctx, "UPDATE node_operation_receipts SET state=?,error=?,result=? WHERE id=? AND state=''",
		receipt.State, receipt.Error, document, receipt.ID)
	return err
}

// RecoverNodeOperations is called once per process start. An interrupted
// mutation is reported as uncertain and is never executed again.
func (l *Ledger) RecoverNodeOperations(ctx context.Context) ([]nodeops.Operation, error) {
	if _, err := l.db.ExecContext(ctx, "UPDATE node_operation_receipts SET state=?,error=? WHERE state=''",
		nodeops.Uncertain, "The node restarted before confirming the Docker operation. Refresh resources before retrying."); err != nil {
		return nil, err
	}
	rows, err := l.db.QueryContext(ctx, "SELECT id,state,error,result FROM node_operation_receipts ORDER BY rowid")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var receipts []nodeops.Operation
	for rows.Next() {
		var receipt nodeops.Operation
		var document []byte
		if err := rows.Scan(&receipt.ID, &receipt.State, &receipt.Error, &document); err != nil {
			return nil, err
		}
		if len(document) != 0 {
			if err := json.Unmarshal(document, &receipt.Result); err != nil {
				return nil, err
			}
		}
		receipts = append(receipts, receipt)
	}
	return receipts, rows.Err()
}

func (l *Ledger) AckNodeOperation(ctx context.Context, id string) error {
	_, err := l.db.ExecContext(ctx, "DELETE FROM node_operation_receipts WHERE id=?", id)
	return err
}
