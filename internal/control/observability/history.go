package observability

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"regexp"
	"time"
)

//go:embed history.sql
var historySchema string

var historyContainerID = regexp.MustCompile(`^[a-f0-9]{64}$`)

const (
	historyRetention     = 7 * 24 * time.Hour
	historySlot          = 30 * time.Second
	maxHistoryContainers = 64
	maxHistoryPoints     = 2_000_000
	historyPruneBatch    = 2000
)

type History struct {
	Points      []HistoryPoint     `json:"points"`
	Containers  []HistoryContainer `json:"containers"`
	StepSeconds int64              `json:"step_seconds"`
}

type HistoryContainer struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type HistoryPoint struct {
	At               time.Time `json:"at"`
	CPUPercent       *float64  `json:"cpu_percent,omitempty"`
	MemoryBytes      *int64    `json:"memory_bytes,omitempty"`
	MemoryLimitBytes *int64    `json:"memory_limit_bytes,omitempty"`
	DiskUsedBytes    *int64    `json:"disk_used_bytes,omitempty"`
	DiskTotalBytes   *int64    `json:"disk_total_bytes,omitempty"`
	RxBytesSec       *float64  `json:"rx_bytes_sec,omitempty"`
	TxBytesSec       *float64  `json:"tx_bytes_sec,omitempty"`
	ReadBytesSec     *float64  `json:"read_bytes_sec,omitempty"`
	WriteBytesSec    *float64  `json:"write_bytes_sec,omitempty"`
}

func historyStep(window time.Duration) time.Duration {
	switch window {
	case time.Hour:
		return 15 * time.Second
	case 6 * time.Hour:
		return time.Minute
	case 24 * time.Hour:
		return 5 * time.Minute
	case historyRetention:
		return 30 * time.Minute
	default:
		return 0
	}
}

// MetricHistory returns the latest observation in each populated bucket. The
// timestamps are the original measurement times, so gaps remain visible.
func (s *Store) MetricHistory(ctx context.Context, agent string, generation int64, container string, window time.Duration, now time.Time) (History, error) {
	step := historyStep(window)
	if !validPeer(agent, generation) || container != "" && !historyContainerID.MatchString(container) || step == 0 || now.UnixMilli() <= window.Milliseconds() {
		return History{}, errors.New("invalid monitoring history query")
	}
	history := History{
		Points:      make([]HistoryPoint, 0),
		Containers:  make([]HistoryContainer, 0),
		StepSeconds: int64(step / time.Second),
	}
	start, end := now.Add(-window).UnixMilli(), now.UnixMilli()
	containers, err := s.historyContainers(ctx, agent, generation, start, end)
	if err != nil {
		return History{}, err
	}
	history.Containers = containers
	rows, err := s.db.QueryContext(ctx, `
WITH source AS (
 SELECT id FROM metric_history_series WHERE agent=? AND generation=? AND container=?
), buckets AS (
 SELECT max(slot_ms) AS slot_ms FROM metric_history_points WHERE series_id=(SELECT id FROM source)
 AND slot_ms BETWEEN ? AND ? AND at_ms>? AND at_ms<=?
 GROUP BY (at_ms-?-1)/?
)
SELECT at_ms,cpu_percent,memory_bytes,memory_limit_bytes,disk_used_bytes,disk_total_bytes,
 rx_bytes_sec,tx_bytes_sec,read_bytes_sec,write_bytes_sec
 FROM buckets JOIN metric_history_points USING (slot_ms)
 WHERE series_id=(SELECT id FROM source) ORDER BY slot_ms`,
		agent, generation, container, metricSlot(start), metricSlot(end), start, end, start, step.Milliseconds())
	if err != nil {
		return History{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var point HistoryPoint
		var at int64
		if err := rows.Scan(&at, &point.CPUPercent, &point.MemoryBytes, &point.MemoryLimitBytes,
			&point.DiskUsedBytes, &point.DiskTotalBytes, &point.RxBytesSec, &point.TxBytesSec,
			&point.ReadBytesSec, &point.WriteBytesSec); err != nil {
			return History{}, err
		}
		point.At = time.UnixMilli(at).UTC()
		history.Points = append(history.Points, point)
	}
	return history, rows.Err()
}

func (s *Store) historyContainers(ctx context.Context, agent string, generation int64, start, end int64) ([]HistoryContainer, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT container,name FROM metric_history_series AS source
 WHERE agent=? AND generation=? AND container<>'' AND first_sample_ms<=? AND last_sample_ms>?
 AND EXISTS (SELECT 1 FROM metric_history_points WHERE series_id=source.id
             AND slot_ms BETWEEN ? AND ? AND at_ms>? AND at_ms<=?)
 ORDER BY name COLLATE NOCASE,container LIMIT ?`, agent, generation, end, start,
		metricSlot(start), metricSlot(end), start, end, maxHistoryContainers)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	containers := make([]HistoryContainer, 0)
	for rows.Next() {
		var container HistoryContainer
		if err := rows.Scan(&container.ID, &container.Name); err != nil {
			return nil, err
		}
		containers = append(containers, container)
	}
	return containers, rows.Err()
}

func metricSlot(at int64) int64 {
	step := historySlot.Milliseconds()
	return at / step * step
}

// PruneMetricHistory keeps at most seven days of history and caps the total
// retained points. Separate batch transactions let inventory reporting continue
// while an accumulated backlog is pruned.
func (s *Store) PruneMetricHistory(ctx context.Context, now time.Time) error {
	return s.pruneMetricHistory(ctx, now, maxHistoryPoints)
}

func (s *Store) pruneMetricHistory(ctx context.Context, now time.Time, maxPoints int) error {
	cutoff := now.Add(-historyRetention).UnixMilli()
	var cutoffRow int64
	err := s.db.QueryRowContext(ctx, `
SELECT at_ms,rowid FROM metric_history_points WHERE at_ms>=?
 ORDER BY at_ms DESC,rowid DESC LIMIT 1 OFFSET ?`, cutoff, maxPoints-1).Scan(&cutoff, &cutoffRow)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err := s.deleteHistoryBatches(ctx, `DELETE FROM metric_history_points WHERE rowid IN (
 SELECT rowid FROM metric_history_points WHERE (at_ms,rowid)<(?,?) ORDER BY at_ms,rowid LIMIT ?)`,
		cutoff, cutoffRow); err != nil {
		return err
	}
	return s.deleteHistoryBatches(ctx, `DELETE FROM metric_history_series WHERE id IN (
 SELECT id FROM metric_history_series AS source WHERE last_sample_ms<=?
 AND NOT EXISTS (SELECT 1 FROM metric_history_points WHERE series_id=source.id)
 ORDER BY last_sample_ms LIMIT ?)`, cutoff)
}

func (s *Store) deleteHistoryBatches(ctx context.Context, statement string, args ...any) error {
	args = append(args, historyPruneBatch)
	for {
		result, err := s.db.ExecContext(ctx, statement, args...)
		if err != nil {
			return err
		}
		deleted, err := result.RowsAffected()
		if err != nil || deleted < historyPruneBatch {
			return err
		}
	}
}
