package storage

import (
	"context"
	"time"
)

type MonitoringEvent struct {
	At    time.Time `json:"at"`
	Kind  string    `json:"kind"`
	Label string    `json:"label"`
}

// MonitoringEvents shows work that actually reached the node. The caller
// authenticates the current generation before reading the timeline.
func (s *Store) MonitoringEvents(ctx context.Context, agent string, generation int64, from, until time.Time) ([]MonitoringEvent, error) {
	if agent == "" || generation <= 0 || !from.Before(until) {
		return nil, ErrInvalid
	}
	const datetime = "2006-01-02 15:04:05"
	rows, err := s.db.QueryContext(ctx, `
		SELECT at_ms,kind,label FROM (
			SELECT unixepoch(a.created_at)*1000 AS at_ms,
			       CASE WHEN d.rollback_of IS NULL THEN 'deployment_' ELSE 'rollback_' END ||
			       CASE WHEN a.state='executing' THEN 'started' ELSE a.state END AS kind,
			       d.project || ' / ' || d.target || ' · ' || d.release_id AS label
			FROM deployments d JOIN deployment_audit a ON a.deployment_id=d.id
			WHERE d.agent=? AND d.generation=? AND a.created_at>=? AND a.created_at<=?
			  AND a.state IN ('executing','succeeded','failed')
			UNION ALL
			SELECT completed_ms,'container_restart',substr(json_extract(request,'$.target'),1,12)
			FROM node_operations
			WHERE agent=? AND generation=? AND action='container.restart' AND state='succeeded'
			  AND completed_ms>=? AND completed_ms<=?
		) ORDER BY at_ms DESC,kind,label LIMIT 100`,
		agent, generation, from.UTC().Format(datetime), until.UTC().Format(datetime),
		agent, generation, from.UnixMilli(), until.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]MonitoringEvent, 0)
	for rows.Next() {
		var event MonitoringEvent
		var millis int64
		if err := rows.Scan(&millis, &event.Kind, &event.Label); err != nil {
			return nil, err
		}
		event.At = time.UnixMilli(millis).UTC()
		out = append(out, event)
	}
	return out, rows.Err()
}
