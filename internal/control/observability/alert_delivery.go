package observability

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const (
	alertDeliveryLifetime = 24 * time.Hour
	alertDeliveryLease    = 30 * time.Second
	maxAlertAttempts      = 8
)

const alertRecipientSchema = `ALTER TABLE alert_deliveries RENAME TO single_alert_deliveries;
CREATE TABLE alert_deliveries (
 event_id INTEGER NOT NULL REFERENCES alert_events(id) ON DELETE CASCADE,
 recipient TEXT NOT NULL DEFAULT '',
 attempts INTEGER NOT NULL DEFAULT 0,
 available_ms INTEGER NOT NULL,
 expires_ms INTEGER NOT NULL,
 PRIMARY KEY (event_id,recipient)
);
INSERT INTO alert_deliveries(event_id,attempts,available_ms,expires_ms)
 SELECT event_id,attempts,available_ms,expires_ms FROM single_alert_deliveries;
DROP TABLE single_alert_deliveries;
CREATE INDEX alert_deliveries_available ON alert_deliveries(available_ms,event_id);
CREATE INDEX alert_deliveries_expiry ON alert_deliveries(expires_ms);`

// AlertDelivery is the event and lease for a single webhook attempt.
type AlertDelivery struct {
	AlertEvent
	Agent      string `json:"agent"`
	Generation int64  `json:"generation"`
	Attempt    int    `json:"-"`
	Recipient  string `json:"-"`
}

// ExpandAlertRecipients resolves the configured destinations once per event.
// The caller supplies stable non-secret identifiers, never destination URLs.
func (s *Store) ExpandAlertRecipients(ctx context.Context, recipients []string, now time.Time) error {
	var pending bool
	if err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM alert_deliveries WHERE recipient='' AND available_ms<=?)", now.UnixMilli()).Scan(&pending); err != nil || !pending {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, recipient := range recipients {
		if _, err := tx.ExecContext(ctx, `INSERT INTO alert_deliveries(event_id,recipient,attempts,available_ms,expires_ms)
 SELECT event_id,?,attempts,available_ms,expires_ms FROM alert_deliveries WHERE recipient='' AND available_ms<=?
 ON CONFLICT DO NOTHING`, recipient, now.UnixMilli()); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM alert_deliveries WHERE recipient='' AND available_ms<=?", now.UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

// ClaimAlertDelivery leases the next eligible notification. Its attempt number
// identifies the lease, so a late result cannot update a newer attempt.
func (s *Store) ClaimAlertDelivery(ctx context.Context, now time.Time) (*AlertDelivery, error) {
	return s.claimAlertDelivery(ctx, now, false)
}

func (s *Store) ClaimAlertRecipient(ctx context.Context, now time.Time) (*AlertDelivery, error) {
	return s.claimAlertDelivery(ctx, now, true)
}

func (s *Store) claimAlertDelivery(ctx context.Context, now time.Time, expanded bool) (*AlertDelivery, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	nowMs := now.UnixMilli()
	if _, err := tx.ExecContext(ctx, "DELETE FROM alert_deliveries WHERE expires_ms<=? OR (attempts>=? AND available_ms<=?)",
		nowMs, maxAlertAttempts, nowMs); err != nil {
		return nil, err
	}
	var delivery AlertDelivery
	var at int64
	err = tx.QueryRowContext(ctx, `SELECT e.id,e.agent,e.generation,e.code,e.state,e.at_ms,d.attempts,d.recipient
	 FROM alert_deliveries d JOIN alert_events e ON e.id=d.event_id
	 WHERE d.available_ms<=? AND d.attempts<? AND (d.recipient<>'')=?
	 AND NOT EXISTS (
	   SELECT 1 FROM alert_events prior JOIN alert_deliveries pd ON pd.event_id=prior.id
	   WHERE prior.agent=e.agent AND prior.generation=e.generation AND prior.code=e.code AND prior.id<e.id
	   AND (pd.recipient=d.recipient OR pd.recipient='')
	 )
	 ORDER BY d.available_ms,d.event_id,d.recipient LIMIT 1`, nowMs, maxAlertAttempts, expanded).Scan(
		&delivery.ID, &delivery.Agent, &delivery.Generation, &delivery.Code, &delivery.State, &at, &delivery.Attempt, &delivery.Recipient)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	delivery.At = time.UnixMilli(at).UTC()
	delivery.Attempt++
	if _, err := tx.ExecContext(ctx, "UPDATE alert_deliveries SET attempts=?,available_ms=? WHERE event_id=? AND recipient=?",
		delivery.Attempt, now.Add(alertDeliveryLease).UnixMilli(), delivery.ID, delivery.Recipient); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &delivery, nil
}

// NextAlertDeliveryWait reports how long the caller may idle before the oldest
// queued delivery becomes eligible. ok is false when no rows are waiting. The
// query is read-only so idle polling never takes the write lock.
func (s *Store) NextAlertDeliveryWait(ctx context.Context, now time.Time) (time.Duration, bool, error) {
	var due sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT min(d.available_ms) FROM alert_deliveries d JOIN alert_events e ON e.id=d.event_id
 WHERE d.attempts<? AND (d.recipient='' OR NOT EXISTS (
 SELECT 1 FROM alert_events prior JOIN alert_deliveries pd ON pd.event_id=prior.id
 WHERE prior.agent=e.agent AND prior.generation=e.generation AND prior.code=e.code AND prior.id<e.id
 AND (pd.recipient=d.recipient OR pd.recipient='')))`, maxAlertAttempts).Scan(&due); err != nil {
		return 0, false, err
	}
	if !due.Valid {
		return 0, false, nil
	}
	wait := time.Duration(due.Int64-now.UnixMilli()) * time.Millisecond
	if wait < 0 {
		wait = 0
	}
	return wait, true, nil
}

// FinishAlertDelivery acknowledges a successful or exhausted delivery, or
// schedules another attempt with capped exponential backoff.
func (s *Store) FinishAlertDelivery(ctx context.Context, event AlertDelivery, now time.Time, delivered bool) error {
	if event.ID <= 0 || event.Attempt < 1 || event.Attempt > maxAlertAttempts {
		return errors.New("invalid alert delivery attempt")
	}
	if delivered || event.Attempt == maxAlertAttempts {
		_, err := s.db.ExecContext(ctx, "DELETE FROM alert_deliveries WHERE event_id=? AND recipient=? AND attempts=?", event.ID, event.Recipient, event.Attempt)
		return err
	}
	delay := min(30*time.Minute, 30*time.Second<<uint(event.Attempt-1))
	_, err := s.db.ExecContext(ctx, "UPDATE alert_deliveries SET available_ms=? WHERE event_id=? AND recipient=? AND attempts=?",
		now.Add(delay).UnixMilli(), event.ID, event.Recipient, event.Attempt)
	return err
}
