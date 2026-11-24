package observability

import (
	"context"
	"time"
)

// alertEventsIndexSchema adds the indexes that keep the alert ledger bounded
// as it accumulates: the delivery suppress scan seeks prior events per
// (agent, generation, code) instead of walking a generation's whole history,
// and the age index drives pruning and the fired-window summary count.
const alertEventsIndexSchema = `
CREATE INDEX alert_events_suppress ON alert_events(agent,generation,code,id);
CREATE INDEX alert_events_at ON alert_events(at_ms);`

// alertEventRetention keeps a month of alert transitions. It comfortably
// exceeds the delivery lifetime, so every deliverable event has already
// reached a terminal state by the time it ages out.
const alertEventRetention = 30 * 24 * time.Hour

const alertEventPruneBatch = 2000

// PruneAlertEvents drops alert events older than the retention window once
// no delivery remains for them. Events with pending deliveries still
// suppress duplicate alerts, so they are kept until every recipient has
// succeeded, been exhausted or expired; pruning only removes ledger rows
// that can no longer influence delivery. Batching lets alert evaluation
// interleave while a first-run backlog drains across prune cycles.
func (s *Store) PruneAlertEvents(ctx context.Context, now time.Time) error {
	cutoff := now.Add(-alertEventRetention).UnixMilli()
	for {
		result, err := s.db.ExecContext(ctx, `DELETE FROM alert_events WHERE id IN (
	 SELECT e.id FROM alert_events e WHERE e.at_ms<?
	 AND NOT EXISTS (SELECT 1 FROM alert_deliveries d WHERE d.event_id=e.id)
	 LIMIT ?)`, cutoff, alertEventPruneBatch)
		if err != nil {
			return err
		}
		deleted, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if deleted < alertEventPruneBatch {
			return nil
		}
	}
}
