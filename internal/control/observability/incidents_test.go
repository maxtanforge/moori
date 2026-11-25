package observability

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/core/telemetry"
)

func TestIncidentsPersistAndRequireFreshRecoveryEvidence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "metrics.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	now := time.Now().UTC().Truncate(time.Millisecond)
	sample := telemetry.Sample{MemoryBytes: 100, MemoryAvailableBytes: 50, DiskBytes: 100, DiskAvailableBytes: 4}
	if err := s.RecordContact(ctx, "agent", 1, now); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordSample(ctx, "agent", 1, sample, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Evaluate(ctx, "agent", 1, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	offline := now.Add(2 * time.Minute)
	if err := s.Evaluate(ctx, "agent", 1, offline); err != nil {
		t.Fatal(err)
	}
	status, err := s.Health(ctx, "agent", 1, offline)
	if err != nil || len(status.Incidents) != 2 || status.Incidents[1].Code != "disk_low" || !status.Incidents[1].OpenedAt.Equal(now) {
		t.Fatalf("stale sample cleared persisted disk incident: %+v %v", status, err)
	}
	if err := s.RecordContact(ctx, "agent", 1, offline); err != nil {
		t.Fatal(err)
	}
	sample.DiskAvailableBytes = 12
	if err := s.RecordSample(ctx, "agent", 1, sample, offline); err != nil {
		t.Fatal(err)
	}
	if err := s.Evaluate(ctx, "agent", 1, offline); err != nil {
		t.Fatal(err)
	}
	status, err = s.Health(ctx, "agent", 1, offline)
	if err != nil || len(status.Incidents) != 1 || status.Incidents[0].Code != "disk_low" {
		t.Fatalf("near-threshold sample cleared disk or did not clear contact incident: %+v %v", status, err)
	}
	recovered := offline.Add(time.Minute)
	if err := s.RecordContact(ctx, "agent", 1, recovered); err != nil {
		t.Fatal(err)
	}
	sample.DiskAvailableBytes = 20
	if err := s.RecordSample(ctx, "agent", 1, sample, recovered); err != nil {
		t.Fatal(err)
	}
	if err := s.Evaluate(ctx, "agent", 1, recovered); err != nil {
		t.Fatal(err)
	}
	status, err = s.Health(ctx, "agent", 1, recovered)
	if err != nil || len(status.Incidents) != 0 {
		t.Fatalf("fresh recovery did not resolve incident: %+v %v", status, err)
	}
}
