package storage

import (
	"context"
	"time"
)

// DeploymentSummary aggregates deployment states platform-wide for the console
// dashboard. Window counts read the audit trail, so they measure state
// transitions: a deployment that failed twice inside the window counts twice.
type DeploymentSummary struct {
	Active          int `json:"active"`
	SucceededWindow int `json:"succeeded_window"`
	FailedWindow    int `json:"failed_window"`
}

// PlatformDeploymentSummary requires a platform administrator session. The
// window starts at windowStart; audit timestamps are stored in the SQLite
// CURRENT_TIMESTAMP format, so the cutoff is bound as the same UTC text.
// The actor check and both aggregations are plain reads on the pool: an
// explicit transaction would pin the database-wide write lock for the
// duration of a dashboard poll without adding consistency the numbers need.
func (s *Store) PlatformDeploymentSummary(ctx context.Context, token string, windowStart time.Time) (DeploymentSummary, error) {
	summary := DeploymentSummary{}
	if _, err := platformActor(ctx, s.db, token); err != nil {
		return summary, err
	}
	states, err := s.db.QueryContext(ctx, `SELECT state,COUNT(*) FROM deployments
 WHERE state IN ('queued','dispatching','executing','verifying','reconciling','needs_attention')
 GROUP BY state`)
	if err != nil {
		return summary, err
	}
	defer states.Close()
	for states.Next() {
		var state string
		var count int
		if err := states.Scan(&state, &count); err != nil {
			return summary, err
		}
		summary.Active += count
	}
	if err := states.Err(); err != nil {
		return summary, err
	}
	states.Close()
	window, err := s.db.QueryContext(ctx, `SELECT state,COUNT(*) FROM deployment_audit
 WHERE state IN ('succeeded','failed') AND created_at>=? GROUP BY state`,
		windowStart.UTC().Format(time.DateTime))
	if err != nil {
		return summary, err
	}
	defer window.Close()
	for window.Next() {
		var state string
		var count int
		if err := window.Scan(&state, &count); err != nil {
			return summary, err
		}
		if state == "succeeded" {
			summary.SucceededWindow = count
		} else {
			summary.FailedWindow = count
		}
	}
	if err := window.Err(); err != nil {
		return summary, err
	}
	return summary, nil
}
