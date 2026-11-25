package observability

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"time"
)

// maxInventoryBytes bounds one stored overview; dispatch applies the same
// budget before handing the snapshot over.
const maxInventoryBytes = 768 << 10

// Inventory is the latest machine resource snapshot an Agent reported.
type Inventory struct {
	Agent      string          `json:"agent"`
	Generation int64           `json:"generation"`
	UpdatedAt  *time.Time      `json:"updated_at,omitempty"`
	Overview   json.RawMessage `json:"overview,omitempty"`
}

// RecordInventory keeps the latest snapshot and records its fresh measurements.
func (s *Store) RecordInventory(ctx context.Context, agent string, generation int64, overview json.RawMessage, at time.Time) error {
	if !validPeer(agent, generation) || at.UnixMilli() <= 0 || !json.Valid(overview) || len(overview) == 0 || len(overview) > maxInventoryBytes {
		return errors.New("invalid Agent inventory")
	}
	var samples []historySample
	if !s.latestOnly {
		samples = prepareMetricHistory(overview, at)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO agent_inventories VALUES (?,?,?,?)
 ON CONFLICT(agent) DO UPDATE SET generation=excluded.generation,at_ms=excluded.at_ms,overview=excluded.overview
 WHERE excluded.generation>agent_inventories.generation OR
 (excluded.generation=agent_inventories.generation AND excluded.at_ms>=agent_inventories.at_ms AND excluded.overview<>agent_inventories.overview)`,
		agent, generation, at.UnixMilli(), string(overview))
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 0 && !s.latestOnly {
		if err := recordMetricHistory(ctx, tx, agent, generation, samples, at); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// AgentInventory returns the latest snapshot reported by an Agent; an Agent
// that never reported yields an Inventory without an overview.
func (s *Store) AgentInventory(ctx context.Context, agent string) (Inventory, error) {
	if agent == "" || len(agent) > 128 {
		return Inventory{}, errors.New("invalid Agent identity")
	}
	var generation, at int64
	var overview string
	err := s.db.QueryRowContext(ctx, `SELECT generation,at_ms,overview FROM agent_inventories WHERE agent=?`, agent).Scan(&generation, &at, &overview)
	if errors.Is(err, sql.ErrNoRows) {
		return Inventory{Agent: agent}, nil
	}
	if err != nil {
		return Inventory{}, err
	}
	updated := time.UnixMilli(at).UTC()
	return Inventory{Agent: agent, Generation: generation, UpdatedAt: &updated, Overview: json.RawMessage(overview)}, nil
}
