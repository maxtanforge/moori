package observability

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/core/telemetry"
)

// The dashboard alert summary counts open incidents regardless of age and
// fired events only inside the lookback window.
func TestAlertSummaryCountsOpenIncidentsAndWindowFirings(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "metrics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	sample := telemetry.Sample{MemoryBytes: 100, MemoryAvailableBytes: 50, DiskBytes: 100, DiskAvailableBytes: 4}
	update := func(agent string, generation int64, at time.Time) {
		t.Helper()
		if err := s.RecordContact(ctx, agent, generation, at); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordSample(ctx, agent, generation, sample, at); err != nil {
			t.Fatal(err)
		}
		if err := s.Evaluate(ctx, agent, generation, at); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	update("a", 1, now)
	update("b", 1, now)
	summary, err := s.AlertSummary(ctx, now, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Active != 2 || summary.FiredWindow != 2 {
		t.Fatalf("summary = %+v, want 2 open incidents, 2 firings", summary)
	}
	// Shifting the reference past the window drops fired events but keeps
	// incidents open until they actually recover.
	summary, err = s.AlertSummary(ctx, now.Add(25*time.Hour), 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Active != 2 || summary.FiredWindow != 0 {
		t.Fatalf("aged summary = %+v, want incidents kept, firings dropped", summary)
	}
	sample.DiskAvailableBytes = 40
	update("a", 1, now.Add(30*time.Second))
	summary, err = s.AlertSummary(ctx, now.Add(30*time.Second), 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Active != 1 || summary.FiredWindow != 2 {
		t.Fatalf("recovered summary = %+v, want 1 open incident", summary)
	}
}
