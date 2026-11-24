package observability

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/core/telemetry"
)

func TestResourceIncidentsRequireSustainedFreshSamples(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "metrics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Millisecond)
	cpu := uint16(9000)
	sample := telemetry.Sample{CPU: &cpu, MemoryBytes: 100, MemoryAvailableBytes: 10, DiskBytes: 100, DiskAvailableBytes: 40}
	evaluate := func(offset time.Duration, update bool, want ...string) {
		t.Helper()
		at := now.Add(offset)
		if err := s.RecordContact(ctx, "agent", 1, at); err != nil {
			t.Fatal(err)
		}
		if update {
			if err := s.RecordSample(ctx, "agent", 1, sample, at); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.Evaluate(ctx, "agent", 1, at); err != nil {
			t.Fatal(err)
		}
		health, err := s.Health(ctx, "agent", 1, at)
		if err != nil || len(health.Incidents) != len(want) {
			t.Fatalf("at %s: incidents = %+v, err = %v, want %v", offset, health.Incidents, err, want)
		}
		for i, code := range want {
			if health.Incidents[i].Code != code {
				t.Fatalf("at %s: incidents = %+v, want %v", offset, health.Incidents, want)
			}
		}
	}
	evaluate(0, true)
	evaluate(time.Minute, false)
	evaluate(2*time.Minute, true) // The gap invalidates the first sample's window.
	evaluate(150*time.Second, true)
	evaluate(180*time.Second, true)
	evaluate(210*time.Second, true)
	evaluate(239*time.Second, false) // Still the sample at 210 seconds.
	evaluate(240*time.Second, true, "cpu_high", "memory_high")
	evaluate(360*time.Second, false, "cpu_high", "memory_high")
	sample.CPU = nil
	sample.MemoryAvailableBytes = 12
	evaluate(370*time.Second, true, "cpu_high", "memory_high")
	sample.CPU = &cpu
	cpu = 8500
	sample.MemoryAvailableBytes = 15
	evaluate(400*time.Second, true)
}

func TestResourceAlertDoesNotMissLowSampleBetweenEvaluations(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "metrics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Millisecond)
	update := func(seconds int, cpu uint16, evaluate bool, wantIncident bool) {
		t.Helper()
		at := now.Add(time.Duration(seconds) * time.Second)
		sample := telemetry.Sample{CPU: &cpu, MemoryBytes: 100, MemoryAvailableBytes: 50, DiskBytes: 100, DiskAvailableBytes: 40}
		if err := s.RecordContact(ctx, "agent", 1, at); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordSample(ctx, "agent", 1, sample, at); err != nil {
			t.Fatal(err)
		}
		if !evaluate {
			return
		}
		if err := s.Evaluate(ctx, "agent", 1, at); err != nil {
			t.Fatal(err)
		}
		health, err := s.Health(ctx, "agent", 1, at)
		if err != nil || (len(health.Incidents) == 1) != wantIncident {
			t.Fatalf("at %ds: incidents = %+v, err = %v", seconds, health.Incidents, err)
		}
	}
	update(0, 9000, true, false)
	update(30, 9000, true, false)
	update(60, 4000, false, false)
	update(90, 9000, false, false)
	update(120, 9000, true, false)
	update(180, 9000, false, false)
	update(240, 9000, true, true)
}
