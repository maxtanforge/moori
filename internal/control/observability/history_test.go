package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/node/localinv"
)

const historyTestContainer = "e8dfd75801b050941a705c519f5129ac7658b1924fe010f226a5b321065b0f9d"

func TestMetricHistoryUsesMeasurementTimestamps(t *testing.T) {
	s := openHistoryTestStore(t)
	at := time.Date(2026, 11, 10, 18, 0, 0, 0, time.UTC)
	cpu := 10.5
	host := &localinv.Host{SampledAt: at, CPUPercent: &cpu, MemoryTotalBytes: 100, MemoryAvailableBytes: 40,
		Interfaces: []localinv.Interface{{Name: "eth0", RxBytes: 100, TxBytes: 200}, {Name: "lo", RxBytes: 900, TxBytes: 900}}}
	stats := &localinv.Stats{SampledAt: at.Add(time.Second), CPUPercent: &cpu, MemoryBytes: 25, MemoryLimitBytes: 50,
		RxBytes: 100, TxBytes: 200, BlockReadBytes: 300, BlockWriteBytes: 400}
	overview := localinv.Overview{GeneratedAt: at, Host: host, SectionTimes: map[string]time.Time{"filesystems": at},
		Filesystems: []localinv.Filesystem{{Mountpoint: "/", TotalBytes: 1000, UsedBytes: 300}},
		Containers:  []localinv.Container{{ID: historyTestContainer[:12], FullID: historyTestContainer, Name: "app", State: "running", Stats: stats}}}
	recordHistoryTestInventory(t, s, 1, overview, at.Add(2*time.Second))

	at = at.Add(33 * time.Second)
	overview.GeneratedAt = at
	host.SampledAt = at
	host.Interfaces = []localinv.Interface{{Name: "eth0", RxBytes: 430, TxBytes: 860}, {Name: "lo", RxBytes: 90000, TxBytes: 90000},
		{Name: "new", RxBytes: 90000, TxBytes: 90000}}
	stats.SampledAt = at.Add(3 * time.Second)
	stats.RxBytes, stats.TxBytes, stats.BlockReadBytes, stats.BlockWriteBytes = 450, 900, 1350, 1800
	recordHistoryTestInventory(t, s, 1, overview, at.Add(4*time.Second))

	// A failed inventory refresh copied the previous resource values verbatim.
	recordHistoryTestInventory(t, s, 1, overview, at.Add(34*time.Second))
	overview.GeneratedAt = at.Add(65 * time.Second)
	recordHistoryTestInventory(t, s, 1, overview, at.Add(66*time.Second))

	now := at.Add(70 * time.Second)
	hostHistory := readHistoryTestSeries(t, s, 1, "", time.Hour, now)
	if len(hostHistory.Points) != 2 || len(hostHistory.Containers) != 1 || hostHistory.Containers[0].ID != historyTestContainer ||
		hostHistory.Containers[0].Name != "app" {
		t.Fatalf("host history: %+v", hostHistory)
	}
	first, second := hostHistory.Points[0], hostHistory.Points[1]
	if first.RxBytesSec != nil || first.MemoryBytes == nil || *first.MemoryBytes != 60 || first.DiskUsedBytes == nil ||
		*first.DiskUsedBytes != 300 || first.DiskTotalBytes == nil || *first.DiskTotalBytes != 1000 ||
		second.DiskUsedBytes != nil || second.RxBytesSec == nil || *second.RxBytesSec != 10 ||
		second.TxBytesSec == nil || *second.TxBytesSec != 20 || !second.At.Equal(at) {
		t.Fatalf("host points: %+v", hostHistory.Points)
	}
	containerHistory := readHistoryTestSeries(t, s, 1, historyTestContainer, time.Hour, now)
	if len(containerHistory.Points) != 2 {
		t.Fatalf("container history: %+v", containerHistory)
	}
	first, second = containerHistory.Points[0], containerHistory.Points[1]
	if first.RxBytesSec != nil || first.MemoryBytes == nil || *first.MemoryBytes != 25 || first.CPUPercent == nil ||
		*first.CPUPercent != cpu || second.RxBytesSec == nil || *second.RxBytesSec != 10 || second.TxBytesSec == nil ||
		*second.TxBytesSec != 20 || second.ReadBytesSec == nil || *second.ReadBytesSec != 30 || second.WriteBytesSec == nil ||
		*second.WriteBytesSec != 40 || !second.At.Equal(stats.SampledAt) {
		t.Fatalf("container points: %+v", containerHistory.Points)
	}
	other := readHistoryTestSeries(t, s, 2, historyTestContainer, time.Hour, now)
	if len(other.Points) != 0 || len(other.Containers) != 0 {
		t.Fatalf("another generation can see history: %+v", other)
	}
}

func TestMetricHistoryOmitsResetAndImplausibleRates(t *testing.T) {
	s := openHistoryTestStore(t)
	at := time.Date(2026, 11, 10, 18, 0, 0, 0, time.UTC)
	host := &localinv.Host{SampledAt: at, UptimeSeconds: 100, Interfaces: []localinv.Interface{{Name: "eth0", RxBytes: 400, TxBytes: 500}}}
	stats := &localinv.Stats{SampledAt: at, RxBytes: 100, TxBytes: 200, BlockReadBytes: 300, BlockWriteBytes: 400}
	overview := localinv.Overview{GeneratedAt: at, Host: host,
		Containers: []localinv.Container{{FullID: historyTestContainer, State: "running", Stats: stats}}}
	recordHistoryTestInventory(t, s, 1, overview, at)

	at = at.Add(30 * time.Second)
	overview.GeneratedAt, host.SampledAt, stats.SampledAt = at, at, at
	host.UptimeSeconds = 2
	host.Interfaces[0].RxBytes, host.Interfaces[0].TxBytes = 500, 600
	stats.RxBytes, stats.TxBytes, stats.BlockReadBytes, stats.BlockWriteBytes = 1, ^uint64(0), 600, 1
	recordHistoryTestInventory(t, s, 1, overview, at)

	hostHistory := readHistoryTestSeries(t, s, 1, "", time.Hour, at)
	if len(hostHistory.Points) != 2 || hostHistory.Points[1].RxBytesSec != nil || hostHistory.Points[1].TxBytesSec != nil {
		t.Fatalf("rates cross a host reboot: %+v", hostHistory)
	}
	containerHistory := readHistoryTestSeries(t, s, 1, historyTestContainer, time.Hour, at)
	if len(containerHistory.Points) != 2 {
		t.Fatalf("container history: %+v", containerHistory)
	}
	point := containerHistory.Points[1]
	if point.RxBytesSec != nil || point.TxBytesSec != nil || point.WriteBytesSec != nil ||
		point.ReadBytesSec == nil || *point.ReadBytesSec != 10 {
		t.Fatalf("bad rates survived a reset: %+v", point)
	}
}

func TestMetricHistoryBoundsContainersBucketsAndRetention(t *testing.T) {
	s := openHistoryTestStore(t)
	start := time.Date(2026, 11, 10, 18, 0, 0, 0, time.UTC)
	overview := localinv.Overview{GeneratedAt: start, Host: &localinv.Host{SampledAt: start, MemoryTotalBytes: 100}}
	overview.Containers = append(overview.Containers, localinv.Container{FullID: "short-id", State: "running", Stats: &localinv.Stats{SampledAt: start}})
	for i := range 80 {
		id := fmt.Sprintf("%064x", i+1)
		overview.Containers = append(overview.Containers, localinv.Container{FullID: id, State: "running", Stats: &localinv.Stats{SampledAt: start}})
	}
	recordHistoryTestInventory(t, s, 1, overview, start)
	history := readHistoryTestSeries(t, s, 1, "", 7*24*time.Hour, start)
	if len(history.Containers) != 64 {
		t.Fatalf("expected 64 indexed containers, got %d", len(history.Containers))
	}
	for _, container := range history.Containers {
		if len(container.ID) != 64 {
			t.Fatalf("invalid container id listed in history: %q", container.ID)
		}
	}
	overview.Containers = nil
	for i := 1; i <= 500; i++ {
		at := start.Add(time.Duration(i) * 30 * time.Second)
		overview.GeneratedAt, overview.Host.SampledAt = at, at
		recordHistoryTestInventory(t, s, 1, overview, at)
	}
	now := overview.GeneratedAt
	history = readHistoryTestSeries(t, s, 1, "", 6*time.Hour, now)
	if len(history.Points) == 0 || len(history.Points) > 360 || history.StepSeconds != 60 {
		t.Fatalf("unbounded history: %d points, %d step", len(history.Points), history.StepSeconds)
	}

	pruneAt := start.Add(7*24*time.Hour + 30*time.Minute)
	if err := s.PruneMetricHistory(context.Background(), pruneAt); err != nil {
		t.Fatal(err)
	}
	history = readHistoryTestSeries(t, s, 1, "", 7*24*time.Hour, pruneAt)
	if len(history.Containers) != 0 || len(history.Points) == 0 || history.Points[0].At.Before(pruneAt.Add(-7*24*time.Hour)) {
		t.Fatalf("incorrect retention: %d points, %d containers", len(history.Points), len(history.Containers))
	}
	history = readHistoryTestSeries(t, s, 1, "", 7*24*time.Hour, start)
	if len(history.Points) != 0 || len(history.Containers) != 0 {
		t.Fatalf("old history still on disk: %+v", history)
	}
}

func openHistoryTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func recordHistoryTestInventory(t *testing.T, s *Store, generation int64, overview localinv.Overview, received time.Time) {
	t.Helper()
	raw, err := json.Marshal(overview)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordInventory(context.Background(), "node", generation, raw, received); err != nil {
		t.Fatal(err)
	}
}

func readHistoryTestSeries(t *testing.T, s *Store, generation int64, container string, window time.Duration, now time.Time) History {
	t.Helper()
	history, err := s.MetricHistory(context.Background(), "node", generation, container, window, now)
	if err != nil {
		t.Fatal(err)
	}
	return history
}

func TestMetricHistoryPrunesToGlobalCapAcrossNodes(t *testing.T) {
	s := openHistoryTestStore(t)
	ctx := context.Background()
	first := time.Date(2026, 11, 10, 18, 0, 0, 0, time.UTC)
	second := first.Add(30 * time.Second)
	for _, agent := range []string{"first", "second"} {
		for _, at := range []time.Time{first, second} {
			raw, err := json.Marshal(localinv.Overview{GeneratedAt: at, Host: &localinv.Host{SampledAt: at, MemoryTotalBytes: 100}})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.RecordInventory(ctx, agent, 1, raw, at); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.pruneMetricHistory(ctx, second, 1); err != nil {
		t.Fatal(err)
	}
	var points, series int
	var retained int64
	if err := s.db.QueryRowContext(ctx, "SELECT count(*), max(at_ms) FROM metric_history_points").Scan(&points, &retained); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM metric_history_series").Scan(&series); err != nil {
		t.Fatal(err)
	}
	if points != 1 || series != 1 || retained != second.UnixMilli() {
		t.Fatalf("retained %d points across %d series at %s", points, series, time.UnixMilli(retained))
	}
}

// An agent clock leading the control plane used to silently drop every
// history point; bounded future skew must be kept, while samples stamped far
// beyond the tolerance stay rejected.
func TestMetricHistoryToleratesLeadingAgentClock(t *testing.T) {
	base := time.Now().Round(time.Second).UTC()
	cpu := 10.5
	overview := func(sampled time.Time) localinv.Overview {
		return localinv.Overview{GeneratedAt: sampled, Host: &localinv.Host{
			SampledAt: sampled, CPUPercent: &cpu, MemoryTotalBytes: 100, MemoryAvailableBytes: 40}}
	}

	s := openHistoryTestStore(t)
	recordHistoryTestInventory(t, s, 1, overview(base.Add(45*time.Second)), base)
	hostHistory := readHistoryTestSeries(t, s, 1, "", time.Hour, base.Add(time.Hour))
	if len(hostHistory.Points) != 1 || hostHistory.Points[0].CPUPercent == nil || *hostHistory.Points[0].CPUPercent != cpu {
		t.Fatalf("leading-clock sample dropped: %+v", hostHistory.Points)
	}

	skewed := openHistoryTestStore(t)
	recordHistoryTestInventory(t, skewed, 1, overview(base.Add(10*time.Minute)), base)
	if history := readHistoryTestSeries(t, skewed, 1, "", time.Hour, base.Add(time.Hour)); len(history.Points) != 0 {
		t.Fatalf("far-future sample accepted: %+v", history.Points)
	}
}
