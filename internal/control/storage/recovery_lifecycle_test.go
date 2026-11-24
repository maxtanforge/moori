package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/core/deployment"
)

func TestReleasedRecoveryTargetCanReplaceAgentAndAbandon(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.BindAgent(ctx, "agent", 1, [32]byte{1}))
	for _, target := range []string{"target", "unreleased"} {
		must(s.RegisterAssignedTarget(ctx, "project", target, "agent", 1))
	}
	snapshot := testSnapshot(t, s, "v1")
	must(s.enterRecovery(ctx))
	epoch, err := s.RecoveryEpoch(ctx)
	must(err)
	must(s.RecordInventory(ctx, epoch, "agent", 1, nil, true))
	must(s.ReleaseRestoredTarget(ctx, epoch, "project", "target", "operator", "Docker inspected"))
	for _, id := range []string{"first", "second"} {
		_, err := s.AdmitSnapshot(ctx, Admission{ID: id, Project: "project", Caller: "ci", Key: id, Target: "target", Release: "release"}, snapshot)
		must(err)
	}
	_, err = s.NextOffer(ctx, "agent", 1, time.Now().Add(-2*time.Minute), time.Minute)
	must(err)
	must(s.BindAgent(ctx, "agent", 2, [32]byte{2}))
	if err := s.ReassignTarget(ctx, "project", "target", "agent", 2); !errors.Is(err, ErrTargetBusy) {
		t.Fatalf("busy target reassigned: %v", err)
	}
	must(s.AbandonFencedDeployment(ctx, "first", "operator", "Docker fenced", true))
	must(s.AbandonFencedDeployment(ctx, "first", "operator", "Docker fenced", true))
	if err := s.ReassignTarget(ctx, "project", "unreleased", "agent", 2); err == nil {
		t.Fatal("unreleased target reassigned")
	}
	// The release and assignment must move in the same transaction as the audit.
	_, err = s.db.Exec(`CREATE TRIGGER reject_reassign BEFORE INSERT ON project_audit WHEN NEW.action='target.reassigned' BEGIN SELECT RAISE(ABORT,'audit failed'); END`)
	must(err)
	if err := s.ReassignTarget(ctx, "project", "target", "agent", 2); err == nil {
		t.Fatal("audit failure ignored")
	}
	var assigned, released int64
	must(s.db.QueryRow(`SELECT generation FROM target_assignments WHERE target='target'`).Scan(&assigned))
	must(s.db.QueryRow(`SELECT generation FROM recovery_target_releases WHERE target='target'`).Scan(&released))
	if assigned != 1 || released != 1 {
		t.Fatalf("partial reassignment: %d %d", assigned, released)
	}
	_, err = s.db.Exec(`DROP TRIGGER reject_reassign`)
	must(err)
	must(s.ReassignTarget(ctx, "project", "target", "agent", 2))
	offer, err := s.NextOffer(ctx, "agent", 2, time.Now(), time.Minute)
	must(err)
	if offer.ID != "second" {
		t.Fatalf("replacement did not resume queue: %+v", offer)
	}
	for i, state := range []deployment.State{deployment.Executing, deployment.Verifying, deployment.Succeeded} {
		must(s.ApplyEvent(ctx, Event{ID: string(state), Execution: offer.Execution, Agent: "agent", Generation: 2, Sequence: int64(i + 1), State: state}))
	}
	// A retained epoch still accepts evidence unknown to the restored database.
	must(s.ApplyEvent(ctx, Event{ID: "late", Execution: "unknown", Agent: "agent", Generation: 2, Sequence: 1, State: deployment.Executing}))
	must(s.BindAgent(ctx, "agent", 3, [32]byte{3}))
	if err := s.ReassignTarget(ctx, "project", "target", "agent", 3); !errors.Is(err, ErrTargetBusy) {
		t.Fatalf("unknown active evidence bypassed: %v", err)
	}
	current, err := s.RecoveryEpoch(ctx)
	must(err)
	if current != epoch {
		t.Fatal("historical epoch removed")
	}
}

func TestReleasedRecoveryTargetReassignmentChecksReplacementEvidence(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.BindAgent(ctx, "agent", 1, [32]byte{1}))
	must(s.BindAgent(ctx, "replacement", 1, [32]byte{2}))
	must(s.RegisterAssignedTarget(ctx, "project", "target", "agent", 1))
	must(s.enterRecovery(ctx))
	epoch, err := s.RecoveryEpoch(ctx)
	must(err)
	must(s.RecordInventory(ctx, epoch, "agent", 1, nil, true))
	must(s.ReleaseRestoredTarget(ctx, epoch, "project", "target", "operator", "Docker inspected"))
	must(s.ApplyEvent(ctx, Event{ID: "unknown-active", Execution: "unknown", Agent: "replacement", Generation: 1, Sequence: 1, State: deployment.Executing}))
	if err := s.ReassignTarget(ctx, "project", "target", "replacement", 1); !errors.Is(err, ErrTargetBusy) {
		t.Fatalf("replacement unknown work bypassed: %v", err)
	}
	must(s.ApplyEvent(ctx, Event{ID: "unknown-done", Execution: "unknown", Agent: "replacement", Generation: 1, Sequence: 2, State: deployment.Succeeded}))
	must(s.ReassignTarget(ctx, "project", "target", "replacement", 1))
	// A second replacement must not require completing recovery all over again.
	must(s.BindAgent(ctx, "replacement", 2, [32]byte{3}))
	must(s.ReassignTarget(ctx, "project", "target", "replacement", 2))
	view, err := s.RecoveryStatus(ctx)
	must(err)
	if view.ReleasedTargets != 1 || view.Epoch != epoch {
		t.Fatalf("lost recovery continuity: %+v", view)
	}
	snapshot := testSnapshot(t, s, "v1")
	_, err = s.AdmitSnapshot(ctx, Admission{ID: "new", Project: "project", Caller: "ci", Key: "new", Target: "target", Release: "release"}, snapshot)
	must(err)
	_, err = s.NextOffer(ctx, "replacement", 2, time.Now(), time.Minute)
	must(err)
}
