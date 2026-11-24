package observability

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"time"

	"github.com/maxtanforge/moori/internal/node/localinv"
)

const (
	maxHistoryByteValue  = 1<<53 - 1 // Exact in both SQLite and the browser.
	maxHistoryRate       = 1 << 40   // Bytes per second.
	maxHistoryRateGap    = 2 * time.Minute
	maxHistoryInterfaces = 256
)

type historyCounterPair struct {
	First  uint64
	Second uint64
}

type historyCounters struct {
	Interfaces map[string]historyCounterPair `json:",omitempty"`
	Hostname   string                        `json:",omitempty"`
	Scope      string                        `json:",omitempty"`
	Uptime     uint64                        `json:",omitempty"`
	Network    *historyCounterPair           `json:",omitempty"`
	IO         *historyCounterPair           `json:",omitempty"`
}

type historySample struct {
	container string
	name      string
	point     HistoryPoint
	counters  historyCounters
}

type historyBaseline struct {
	at       int64
	counters historyCounters
}

func prepareMetricHistory(raw json.RawMessage, received time.Time) []historySample {
	var overview localinv.Overview
	if json.Unmarshal(raw, &overview) != nil {
		// RecordInventory has historically accepted arbitrary JSON snapshots.
		return nil
	}
	var samples []historySample
	if host := overview.Host; host != nil && freshHistorySample(host.SampledAt, overview.GeneratedAt, received) {
		sample := historySample{
			point: HistoryPoint{
				At:         host.SampledAt,
				CPUPercent: historyCPU(host.CPUPercent, 100),
			},
			counters: historyCounters{
				Hostname: host.Hostname,
				Scope:    host.Scope,
				Uptime:   host.UptimeSeconds,
			},
		}
		if host.MemoryTotalBytes > 0 && host.MemoryAvailableBytes <= host.MemoryTotalBytes {
			sample.point.MemoryLimitBytes = historyBytes(host.MemoryTotalBytes)
			if sample.point.MemoryLimitBytes != nil {
				sample.point.MemoryBytes = historyBytes(host.MemoryTotalBytes - host.MemoryAvailableBytes)
			}
		}
		if overview.SectionTimes["filesystems"].Equal(host.SampledAt) {
			sample.point.DiskUsedBytes, sample.point.DiskTotalBytes = historyFilesystemUsage(overview.Filesystems)
		}
		for _, device := range host.Interfaces {
			if device.Name == "" || device.Name == "lo" || len(device.Name) > 128 {
				continue
			}
			if sample.counters.Interfaces == nil {
				sample.counters.Interfaces = make(map[string]historyCounterPair)
			}
			sample.counters.Interfaces[device.Name] = historyCounterPair{First: device.RxBytes, Second: device.TxBytes}
			if len(sample.counters.Interfaces) == maxHistoryInterfaces {
				break
			}
		}
		if sample.point.CPUPercent != nil || sample.point.MemoryBytes != nil ||
			sample.point.DiskUsedBytes != nil || len(sample.counters.Interfaces) != 0 {
			samples = append(samples, sample)
		}
	}

	for _, container := range overview.Containers {
		stats := container.Stats
		if stats == nil || !historyContainerID.MatchString(container.FullID) ||
			!freshHistorySample(stats.SampledAt, overview.GeneratedAt, received) {
			continue
		}
		name := container.Name
		if name == "" || len(name) > 128 {
			name = container.FullID
		}
		samples = append(samples, historySample{
			container: container.FullID,
			name:      name,
			point: HistoryPoint{
				At:               stats.SampledAt,
				CPUPercent:       historyCPU(stats.CPUPercent, 1e6),
				MemoryBytes:      historyBytes(stats.MemoryBytes),
				MemoryLimitBytes: historyMemoryLimit(stats.MemoryLimitBytes),
			},
			counters: historyCounters{
				Network: &historyCounterPair{First: stats.RxBytes, Second: stats.TxBytes},
				IO:      &historyCounterPair{First: stats.BlockReadBytes, Second: stats.BlockWriteBytes},
			},
		})
	}
	return samples
}

// historyFutureSkew tolerates agent clocks that lead the control plane.
// Rejecting such samples dropped every history point on unsynchronized hosts,
// while points stamped slightly into the future just land in slots that
// become visible once real time catches up.
const historyFutureSkew = 2 * time.Minute

func freshHistorySample(sampled, generated, received time.Time) bool {
	return sampled.UnixMilli() > 0 && !sampled.Before(generated) &&
		!sampled.Before(received.Add(-freshness)) &&
		!sampled.After(received.Add(historySlot+historyFutureSkew))
}

func historyCPU(cpu *float64, maxPercent float64) *float64 {
	if cpu == nil || math.IsNaN(*cpu) || *cpu < 0 || *cpu > maxPercent {
		return nil
	}
	return cpu
}

func historyBytes(size uint64) *int64 {
	if size > maxHistoryByteValue {
		return nil
	}
	value := int64(size)
	return &value
}

func historyMemoryLimit(size uint64) *int64 {
	if size == 0 {
		return nil
	}
	return historyBytes(size)
}

func historyFilesystemUsage(filesystems []localinv.Filesystem) (used, total *int64) {
	var usedBytes, totalBytes uint64
	for _, fs := range filesystems {
		if fs.UsageUnavailable || fs.UsedBytes > fs.TotalBytes {
			continue
		}
		if fs.TotalBytes > maxHistoryByteValue-totalBytes {
			return nil, nil
		}
		usedBytes += fs.UsedBytes
		totalBytes += fs.TotalBytes
	}
	if totalBytes == 0 {
		return nil, nil
	}
	return historyBytes(usedBytes), historyBytes(totalBytes)
}

func recordMetricHistory(ctx context.Context, tx *sql.Tx, agent string, generation int64, samples []historySample, received time.Time) error {
	if len(samples) == 0 {
		return nil
	}
	baselines, activeContainers, err := historyBaselines(ctx, tx, agent, generation, received.Add(-historyRetention).UnixMilli())
	if err != nil {
		return err
	}
	for _, sample := range samples {
		at := sample.point.At.UnixMilli()
		baseline, known := baselines[sample.container]
		if at <= baseline.at || (!known && sample.container != "" && activeContainers >= maxHistoryContainers) {
			continue
		}
		sample.applyRates(baseline)
		if err := sample.store(ctx, tx, agent, generation); err != nil {
			return err
		}
		if !known && sample.container != "" {
			activeContainers++
		}
		baselines[sample.container] = historyBaseline{at: at, counters: sample.counters}
	}
	return nil
}

func historyBaselines(ctx context.Context, tx *sql.Tx, agent string, generation, since int64) (map[string]historyBaseline, int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT container,last_sample_ms,counters FROM metric_history_series
 WHERE agent=? AND generation=? AND last_sample_ms>=?`, agent, generation, since)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	baselines := make(map[string]historyBaseline)
	activeContainers := 0
	for rows.Next() {
		var container string
		var counters []byte
		var baseline historyBaseline
		if err := rows.Scan(&container, &baseline.at, &counters); err != nil {
			return nil, 0, err
		}
		if err := json.Unmarshal(counters, &baseline.counters); err != nil {
			return nil, 0, err
		}
		baselines[container] = baseline
		if container != "" {
			activeContainers++
		}
	}
	return baselines, activeContainers, rows.Err()
}

func (sample *historySample) store(ctx context.Context, tx *sql.Tx, agent string, generation int64) error {
	at := sample.point.At.UnixMilli()
	counters, err := json.Marshal(sample.counters)
	if err != nil {
		return err
	}
	var seriesID int64
	err = tx.QueryRowContext(ctx, `INSERT INTO metric_history_series(agent,generation,container,name,first_sample_ms,last_sample_ms,counters)
 VALUES (?,?,?,?,?,?,?) ON CONFLICT(agent,generation,container) DO UPDATE SET
 name=excluded.name,last_sample_ms=excluded.last_sample_ms,counters=excluded.counters RETURNING id`,
		agent, generation, sample.container, sample.name, at, at, counters).Scan(&seriesID)
	if err != nil {
		return err
	}
	p := sample.point
	_, err = tx.ExecContext(ctx, `INSERT INTO metric_history_points VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
 ON CONFLICT(series_id,slot_ms) DO UPDATE SET at_ms=excluded.at_ms,cpu_percent=excluded.cpu_percent,
 memory_bytes=excluded.memory_bytes,memory_limit_bytes=excluded.memory_limit_bytes,
 disk_used_bytes=excluded.disk_used_bytes,disk_total_bytes=excluded.disk_total_bytes,rx_bytes_sec=excluded.rx_bytes_sec,
 tx_bytes_sec=excluded.tx_bytes_sec,read_bytes_sec=excluded.read_bytes_sec,write_bytes_sec=excluded.write_bytes_sec`,
		seriesID, metricSlot(at), at, p.CPUPercent, p.MemoryBytes, p.MemoryLimitBytes,
		p.DiskUsedBytes, p.DiskTotalBytes, p.RxBytesSec, p.TxBytesSec, p.ReadBytesSec, p.WriteBytesSec)
	return err
}

func (sample *historySample) applyRates(previous historyBaseline) {
	interval := sample.point.At.UnixMilli() - previous.at
	if previous.at == 0 || interval <= 0 || interval > maxHistoryRateGap.Milliseconds() {
		return
	}
	seconds := float64(interval) / 1000
	current, old := sample.counters, previous.counters
	if sample.container != "" {
		sample.point.RxBytesSec, sample.point.TxBytesSec = historyPairRates(old.Network, current.Network, seconds)
		sample.point.ReadBytesSec, sample.point.WriteBytesSec = historyPairRates(old.IO, current.IO, seconds)
		return
	}
	if old.Hostname != current.Hostname || old.Scope != current.Scope ||
		(old.Uptime > 0 && current.Uptime > 0 && current.Uptime < old.Uptime) {
		return
	}
	sample.point.RxBytesSec, sample.point.TxBytesSec = historyHostNetworkRates(old.Interfaces, current.Interfaces, seconds)
}

func historyPairRates(old, current *historyCounterPair, seconds float64) (first, second *float64) {
	if old == nil || current == nil {
		return nil, nil
	}
	return historyCounterRate(old.First, current.First, seconds), historyCounterRate(old.Second, current.Second, seconds)
}

func historyHostNetworkRates(previous, current map[string]historyCounterPair, seconds float64) (rx, tx *float64) {
	var r, t float64
	var haveRx, haveTx bool
	for name, now := range current {
		if old, ok := previous[name]; ok {
			if rate := historyCounterRate(old.First, now.First, seconds); rate != nil {
				r += *rate
				haveRx = true
			}
			if rate := historyCounterRate(old.Second, now.Second, seconds); rate != nil {
				t += *rate
				haveTx = true
			}
		}
	}
	if haveRx && r <= maxHistoryRate {
		rx = &r
	}
	if haveTx && t <= maxHistoryRate {
		tx = &t
	}
	return rx, tx
}

func historyCounterRate(old, current uint64, seconds float64) *float64 {
	if current < old {
		return nil
	}
	rate := float64(current-old) / seconds
	if rate > maxHistoryRate {
		return nil
	}
	return &rate
}
