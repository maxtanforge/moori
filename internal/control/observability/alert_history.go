package observability

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"time"
)

//go:embed alert_history.sql
var alertSchema string

const alertHistoryLimit = 100

// AlertEvent is a durable transition of a single incident.
type AlertEvent struct {
	ID    int64     `json:"id"`
	Code  string    `json:"code"`
	State string    `json:"state"`
	At    time.Time `json:"at"`
}

// AlertHistory returns the latest transitions for one Agent generation, newest first.
func (s *Store) AlertHistory(ctx context.Context, agent string, generation int64, limit int) ([]AlertEvent, error) {
	if !validPeer(agent, generation) {
		return nil, errors.New("invalid Agent identity")
	}
	if limit <= 0 || limit > alertHistoryLimit {
		limit = alertHistoryLimit
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,code,state,at_ms FROM alert_events
	 WHERE agent=? AND generation=? ORDER BY id DESC LIMIT ?`, agent, generation, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]AlertEvent, 0, limit)
	for rows.Next() {
		var event AlertEvent
		var at int64
		if err := rows.Scan(&event.ID, &event.Code, &event.State, &at); err != nil {
			return nil, err
		}
		event.At = time.UnixMilli(at).UTC()
		events = append(events, event)
	}
	return events, rows.Err()
}

func recordAlertEvent(ctx context.Context, tx *sql.Tx, agent string, generation int64, code, state string, at time.Time, notify bool) error {
	result, err := tx.ExecContext(ctx, `INSERT INTO alert_events(agent,generation,code,state,at_ms)
	 VALUES (?,?,?,?,?)`, agent, generation, code, state, at.UnixMilli())
	if err != nil || !notify {
		return err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO alert_deliveries(event_id,available_ms,expires_ms) VALUES (?,?,?)",
		id, at.UnixMilli(), at.Add(alertDeliveryLifetime).UnixMilli())
	return err
}
