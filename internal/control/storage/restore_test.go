package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/core/deployment"
)

func TestRestoreHoldsDispatchAndQuarantinesPostBackupEvidence(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	live, backup, restored := filepath.Join(dir, "live.db"), filepath.Join(dir, "backup.db"), filepath.Join(dir, "restored.db")
	s := openTestStore(t, live)
	if err := s.BindAgent(ctx, "agent", 1, [32]byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterAssignedTarget(ctx, "project", "target", "agent", 1); err != nil {
		t.Fatal(err)
	}
	snap := testSnapshot(t, s, "v1")
	now := time.Now()
	credential, err := s.IssueServiceCredential(ctx, "ci", "project", "target", now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"active", "queued"} {
		if _, err := s.AdmitServiceSnapshot(ctx, credential, Admission{ID: id, Project: "project", Target: "target", Release: "release", Key: id}, snap); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.NextOffer(ctx, "agent", 1, now, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := BackupDatabase(ctx, live, backup); err != nil {
		t.Fatal(err)
	}
	if err := PrepareRestore(ctx, backup, restored); err != nil {
		t.Fatal(err)
	}
	if err := VerifyBackup(ctx, backup); err != nil {
		t.Fatalf("backup modified by restore: %v", err)
	}
	restoredStore := openTestStore(t, restored)
	if _, err := restoredStore.NextOffer(ctx, "agent", 1, now, time.Minute); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("restored scheduler dispatched: %v", err)
	}
	if _, err := restoredStore.AdmitSnapshot(ctx, Admission{ID: "new", Project: "project", Caller: "human", Target: "target", Release: "release", Key: "new"}, snap); !errors.Is(err, ErrRecovery) {
		t.Fatalf("restore accepted admission: %v", err)
	}
	if _, err := restoredStore.ReadServiceDeployment(ctx, credential, "active"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("restored credential still valid: %v", err)
	}
	for id, want := range map[string]deployment.State{"active": deployment.Reconciling, "queued": deployment.Canceled} {
		view, err := restoredStore.Get(ctx, id)
		if err != nil || view.State != want {
			t.Fatalf("%s: %+v %v", id, view, err)
		}
	}
	report := Event{ID: "post-backup-event", Execution: "unknown-execution", Agent: "agent", Generation: 1, Sequence: 8, State: deployment.Succeeded}
	for range 2 {
		if err := restoredStore.ApplyEvent(ctx, report); err != nil {
			t.Fatal(err)
		}
	}
	report.State = deployment.Failed
	if err := restoredStore.ApplyEvent(ctx, report); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed recovery evidence accepted: %v", err)
	}
	view, err := restoredStore.RecoveryStatus(ctx)
	if err != nil || view.Epoch == "" || view.Targets != 1 || view.Reports != 1 {
		t.Fatalf("recovery status: %+v %v", view, err)
	}
	record := deployment.Evidence{Execution: "post-backup-completed", Target: "target", Digest: snap.Digest(), Sequence: 3, State: deployment.Succeeded}
	if err := restoredStore.RecordInventory(ctx, "old-epoch", "agent", 1, []deployment.Evidence{record}, true); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale recovery epoch accepted: %v", err)
	}
	for range 2 {
		if err := restoredStore.RecordInventory(ctx, view.Epoch, "agent", 1, []deployment.Evidence{record}, true); err != nil {
			t.Fatal(err)
		}
	}
	view, err = restoredStore.RecoveryStatus(ctx)
	if err != nil || view.ObservedExecutions != 1 || view.InventoriedAgents != 1 {
		t.Fatalf("inventory status: %+v %v", view, err)
	}
	inventory, err := restoredStore.ReadRecoveryInventory(ctx, "agent", "")
	if err != nil || !inventory.Complete || len(inventory.Records) != 1 || inventory.Records[0] != record {
		t.Fatalf("operator inventory differs from received evidence: %+v %v", inventory, err)
	}
	held, err := restoredStore.Get(ctx, "active")
	if err != nil || held.State != deployment.Reconciling || held.LastSuccess != "" {
		t.Fatalf("inventory changed deployment facts: %+v %v", held, err)
	}
}
