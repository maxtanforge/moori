package observability

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/core/telemetry"
)

func TestHealthSeparatesContactFreshnessFromResources(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "metrics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Millisecond)
	if health, err := s.Health(ctx, "agent", 1, now); err != nil || health.Contact != "never_seen" || health.Metrics != "missing" {
		t.Fatalf("initial health: %+v %v", health, err)
	}
	cpu := uint16(6000)
	sample := telemetry.Sample{CPU: &cpu, MemoryBytes: 100, MemoryAvailableBytes: 50, DiskBytes: 100, DiskAvailableBytes: 10}
	if err := s.RecordContact(ctx, "agent", 1, now); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordSample(ctx, "agent", 1, sample, now); err != nil {
		t.Fatal(err)
	}
	if health, err := s.Health(ctx, "agent", 1, now); err != nil || health.Contact != "online" || health.Metrics != "current" ||
		len(health.Alerts) != 1 || health.Alerts[0] != "disk_low" {
		t.Fatalf("current health: %+v %v", health, err)
	}
	if err := s.RecordContact(ctx, "agent", 1, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if health, err := s.Health(ctx, "agent", 1, now.Add(2*time.Minute)); err != nil || health.Contact != "online" || health.Metrics != "stale" || len(health.Alerts) != 0 {
		t.Fatalf("stale sample: %+v %v", health, err)
	}
	if health, err := s.Health(ctx, "agent", 1, now.Add(4*time.Minute)); err != nil || health.Contact != "offline" || len(health.Alerts) != 1 || health.Alerts[0] != "agent_offline" {
		t.Fatalf("offline: %+v %v", health, err)
	}
	if health, err := s.Health(ctx, "agent", 2, now.Add(4*time.Minute)); err != nil || health.Contact != "never_seen" || health.Sample != nil {
		t.Fatalf("old generation appeared as current: %+v %v", health, err)
	}
	sample.DiskAvailableBytes = 101
	if err := s.RecordSample(ctx, "agent", 1, sample, now); err == nil {
		t.Fatal("invalid sample accepted")
	}
}
