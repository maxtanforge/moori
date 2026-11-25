package observability

import (
	"context"
	"time"
)

// AlertSummary counts currently open incidents and the alert events fired
// inside the lookback window, for the console dashboard.
type AlertSummary struct {
	Active      int `json:"active"`
	FiredWindow int `json:"fired_window"`
}

func (s *Store) AlertSummary(ctx context.Context, now time.Time, window time.Duration) (AlertSummary, error) {
	summary := AlertSummary{}
	cutoff := now.Add(-window).UnixMilli()
	err := s.db.QueryRowContext(ctx,
		`SELECT (SELECT COUNT(*) FROM active_incidents),(SELECT COUNT(*) FROM alert_events WHERE state='fired' AND at_ms>=?)`,
		cutoff).Scan(&summary.Active, &summary.FiredWindow)
	return summary, err
}
