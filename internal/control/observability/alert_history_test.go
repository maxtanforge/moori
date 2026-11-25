package observability

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/core/telemetry"
	"github.com/maxtanforge/moori/internal/platform/sqlite"
)

func TestAlertHistoryKeepsTransitionsByGeneration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "metrics.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	now := time.Now().UTC().Truncate(time.Millisecond)
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
	update("a", 1, now)
	update("a", 1, now)
	update("b", 1, now)
	recovery := now.Add(30 * time.Second)
	sample.DiskAvailableBytes = 40
	update("a", 1, recovery)
	sample.DiskAvailableBytes = 4
	update("a", 1, recovery.Add(30*time.Second))
	if err := s.Evaluate(ctx, "a", 2, recovery.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.Evaluate(ctx, "a", 1, recovery.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	history, err := s.AlertHistory(ctx, "a", 1, 10)
	if err != nil || len(history) != 3 {
		t.Fatalf("history = %+v, err = %v", history, err)
	}
	if history[0].Code != "disk_low" || history[0].State != "fired" ||
		history[1].State != "recovered" || !history[1].At.Equal(recovery) ||
		history[2].State != "fired" || !history[2].At.Equal(now) ||
		history[0].ID <= history[1].ID || history[1].ID <= history[2].ID || history[2].ID <= 0 {
		t.Fatalf("incorrect transitions: %+v", history)
	}
	health, err := s.Health(ctx, "a", 1, recovery.Add(time.Minute))
	if err != nil || len(health.Incidents) != 0 {
		t.Fatalf("old generation still active: %+v %v", health, err)
	}
	history, err = s.AlertHistory(ctx, "a", 2, 10)
	if err != nil || history == nil || len(history) != 0 {
		t.Fatalf("new generation history = %+v, err = %v", history, err)
	}
}

func TestAlertHistoryPreservesIncidentsFromPreviousSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "metrics.db")
	db, err := sqlite.Open(ctx, path, 0x4d4f4f4d, schema, incidentSchema, inventorySchema)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	_, insertErr := db.ExecContext(ctx, "INSERT INTO active_incidents VALUES ('agent',1,'disk_low',?)", now.UnixMilli())
	db.Close()
	if insertErr != nil {
		t.Fatal(insertErr)
	}
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	history, err := s.AlertHistory(ctx, "agent", 1, 100)
	if err != nil || len(history) != 1 || history[0].State != "fired" || !history[0].At.Equal(now) {
		t.Fatalf("migrated history = %+v, err = %v", history, err)
	}
}

func TestAlertHistoryClampsToLatest100Transitions(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "metrics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.db.ExecContext(ctx, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<110)
	 INSERT INTO alert_events(agent,generation,code,state,at_ms) SELECT 'a',1,'disk_low','fired',i FROM n`)
	if err != nil {
		t.Fatal(err)
	}
	history, err := s.AlertHistory(ctx, "a", 1, 999)
	if err != nil || len(history) != 100 || history[0].ID != 110 || history[99].ID != 11 {
		t.Fatalf("history length = %d, err = %v", len(history), err)
	}
}

func TestAlertEventRollsBackIfNotificationCannotBeQueued(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "metrics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Millisecond)
	_, err = s.db.ExecContext(ctx, `CREATE TRIGGER unavailable_queue BEFORE INSERT ON alert_deliveries
	 BEGIN SELECT RAISE(ABORT, 'forced failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	sample := telemetry.Sample{MemoryBytes: 100, MemoryAvailableBytes: 50, DiskBytes: 100, DiskAvailableBytes: 4}
	if err := s.RecordContact(ctx, "agent", 1, now); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordSample(ctx, "agent", 1, sample, now); err != nil {
		t.Fatal(err)
	}
	policy := DefaultPolicy()
	policy.Notify = true
	if err := s.EvaluateWithPolicy(ctx, "agent", 1, now, policy); err == nil {
		t.Fatal("evaluation unexpectedly succeeded")
	}
	health, err := s.Health(ctx, "agent", 1, now)
	if err != nil || len(health.Incidents) != 0 {
		t.Fatalf("incident survived rollback: %+v, err = %v", health.Incidents, err)
	}
	history, err := s.AlertHistory(ctx, "agent", 1, 100)
	if err != nil || len(history) != 0 {
		t.Fatalf("event survived rollback: %+v, err = %v", history, err)
	}
}
