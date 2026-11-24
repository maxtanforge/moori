package storage

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/core/deployment"
)

func TestFencedAbandonmentRequiresGenerationAndReleasesSlot(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.BindAgent(ctx, "agent", 1, [32]byte{1}))
	must(s.RegisterAssignedTarget(ctx, "project", "target", "agent", 1))
	snapshot := testSnapshot(t, s, "v1")
	for _, id := range []string{"first", "second"} {
		_, err := s.AdmitSnapshot(ctx, Admission{ID: id, Project: "project", Caller: "ci", Key: id, Target: "target", Release: "release"}, snapshot)
		must(err)
	}
	_, err := s.NextOffer(ctx, "agent", 1, time.Now().Add(-2*time.Minute), time.Minute)
	must(err)
	if err := s.AbandonFencedDeployment(ctx, "first", "operator", "stopped host", true); !errors.Is(err, ErrForbidden) {
		t.Fatal("unfenced generation accepted", err)
	}
	must(s.BindAgent(ctx, "agent", 2, [32]byte{2}))
	_, err = s.db.Exec(`UPDATE deployments SET expires_ms=? WHERE id='first'`, time.Now().Add(time.Hour).UnixMilli())
	must(err)
	if err := s.AbandonFencedDeployment(ctx, "first", "operator", "stopped host", true); !errors.Is(err, ErrTargetBusy) {
		t.Fatal("unexpired offer released", err)
	}
	_, err = s.db.Exec(`UPDATE deployments SET expires_ms=? WHERE id='first'`, time.Now().Add(-time.Hour).UnixMilli())
	must(err)
	if err := s.AbandonFencedDeployment(ctx, "first", "operator", "stopped host", false); !errors.Is(err, ErrInvalid) {
		t.Fatal("missing physical fence accepted", err)
	}
	if err := s.ReassignTarget(ctx, "project", "target", "agent", 2); !errors.Is(err, ErrTargetBusy) {
		t.Fatal("busy reassignment", err)
	}
	_, err = s.db.Exec(`CREATE TRIGGER reject_abandon_audit BEFORE INSERT ON project_audit WHEN NEW.action='deployment.abandoned' BEGIN SELECT RAISE(ABORT,'audit failed'); END`)
	must(err)
	if err := s.AbandonFencedDeployment(ctx, "first", "operator", "stopped host", true); err == nil {
		t.Fatal("audit failure ignored")
	}
	var slots int
	must(s.db.QueryRow(`SELECT count(*) FROM target_slots WHERE deployment_id='first'`).Scan(&slots))
	if slots != 1 {
		t.Fatal("failed transaction released slot")
	}
	_, err = s.db.Exec(`DROP TRIGGER reject_abandon_audit`)
	must(err)
	must(s.AbandonFencedDeployment(ctx, "first", "operator", "stopped host", true))
	must(s.AbandonFencedDeployment(ctx, "first", "operator", "stopped host", true))
	if err := s.AbandonFencedDeployment(ctx, "first", "operator", "different", true); !errors.Is(err, ErrConflict) {
		t.Fatal("changed replay", err)
	}
	if err := s.ValidateAgent(ctx, "agent", 1); err == nil {
		t.Fatal("old peer still authorized")
	}
	if err := s.ReassignTarget(ctx, "project", "target", "agent", 1); !errors.Is(err, ErrForbidden) {
		t.Fatal("stale generation assigned", err)
	}
	must(s.ReassignTarget(ctx, "project", "target", "agent", 2))
	next, err := s.NextOffer(ctx, "agent", 2, time.Now(), time.Minute)
	must(err)
	if next.ID != "second" {
		t.Fatal(next.ID)
	}
	view, err := s.Get(ctx, "first")
	must(err)
	if view.State != deployment.Abandoned {
		t.Fatal(view.State)
	}
	var audits int
	must(s.db.QueryRow(`SELECT count(*) FROM project_audit WHERE action='deployment.abandoned'`).Scan(&audits))
	if audits != 1 {
		t.Fatal(audits)
	}
	must(s.enterRecovery(ctx))
	if err := s.AbandonFencedDeployment(ctx, "second", "operator", "stopped host", true); !errors.Is(err, ErrRecovery) {
		t.Fatal("recovery bypassed", err)
	}
}

func TestOutboxRetentionPreservesReceiptsAndRecentEvents(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.RegisterTarget(ctx, "project", "target"))
	must(s.AssignTarget(ctx, "target", "agent", 1))
	snapshot := testSnapshot(t, s, "v1")
	for _, id := range []string{"old", "recent"} {
		_, err := s.AdmitSnapshot(ctx, Admission{ID: id, Project: "project", Caller: "ci", Key: id, Target: "target", Release: "release"}, snapshot)
		must(err)
		_, err = s.NextOffer(ctx, "agent", 1, time.Now(), time.Minute)
		must(err)
		must(s.ApplyEvent(ctx, Event{ID: id + "-terminal", Execution: id, Agent: "agent", Generation: 1, Sequence: 1, State: deployment.Canceled}))
	}
	_, err := s.db.Exec(`UPDATE deployment_audit SET created_at='2000-01-01 00:00:00' WHERE deployment_id='old'`)
	must(err)
	must(s.PruneOutbox(ctx, time.Now().Add(-24*time.Hour)))
	var remaining int
	must(s.db.QueryRow(`SELECT count(*) FROM outbox`).Scan(&remaining))
	if remaining != 1 {
		t.Fatal(remaining)
	}
	var id string
	must(s.db.QueryRow(`SELECT deployment_id FROM outbox`).Scan(&id))
	if id != "recent" {
		t.Fatal(id)
	}
	must(s.ApplyEvent(ctx, Event{ID: "old-terminal", Execution: "old", Agent: "agent", Generation: 1, Sequence: 1, State: deployment.Canceled}))
	var count int
	must(s.db.QueryRow(`SELECT count(*) FROM receipts`).Scan(&count))
	if count != 2 {
		t.Fatal(count)
	}
}

func TestIdlePollDoesNotAcquireWriter(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")
	s := openTestStore(t, path)
	other := openTestStore(t, path)
	if _, err := s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	tx, err := other.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	pollCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	for i := 0; i < 100; i++ {
		if _, err := s.NextOffer(pollCtx, "idle", 1, time.Now(), time.Minute); !errors.Is(err, sql.ErrNoRows) {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(path + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Fatal("idle polls modified WAL")
	}
}

func TestPruneAuditDropsOnlyExpiredRows(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.RegisterTarget(ctx, "project", "target"))
	must(s.AssignTarget(ctx, "target", "agent", 1))
	snapshot := testSnapshot(t, s, "v1")
	for _, id := range []string{"old", "recent"} {
		_, err := s.AdmitSnapshot(ctx, Admission{ID: id, Project: "project", Caller: "ci", Key: id, Target: "target", Release: "release"}, snapshot)
		must(err)
		_, err = s.NextOffer(ctx, "agent", 1, time.Now(), time.Minute)
		must(err)
		must(s.ApplyEvent(ctx, Event{ID: id + "-terminal", Execution: id, Agent: "agent", Generation: 1, Sequence: 1, State: deployment.Canceled}))
	}
	_, err := s.db.Exec(`UPDATE deployment_audit SET created_at='2000-01-01 00:00:00' WHERE deployment_id='old'`)
	must(err)
	_, err = s.db.Exec(`UPDATE project_audit SET created_at='2000-01-01 00:00:00' WHERE subject='old'`)
	must(err)
	must(s.PruneAudit(ctx, time.Now().Add(-24*time.Hour)))
	var count int
	must(s.db.QueryRow(`SELECT count(*) FROM deployment_audit WHERE deployment_id='old'`).Scan(&count))
	if count != 0 {
		t.Fatal(count)
	}
	must(s.db.QueryRow(`SELECT count(*) FROM deployment_audit WHERE deployment_id='recent'`).Scan(&count))
	if count == 0 {
		t.Fatal("recent deployment audit pruned")
	}
	must(s.db.QueryRow(`SELECT count(*) FROM project_audit WHERE subject='old'`).Scan(&count))
	if count != 0 {
		t.Fatal(count)
	}
	must(s.db.QueryRow(`SELECT count(*) FROM project_audit WHERE subject='recent'`).Scan(&count))
	if count == 0 {
		t.Fatal("recent project audit pruned")
	}
	if err := s.PruneAudit(ctx, time.Time{}); err != ErrInvalid {
		t.Fatal(err)
	}
	if err := s.PruneAudit(ctx, time.Now().Add(time.Hour)); err != ErrInvalid {
		t.Fatal(err)
	}
}
