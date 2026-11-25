package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/core/deployment"
	"github.com/maxtanforge/moori/internal/core/release"
)

func TestRestoreReleasesOnlyVerifiedTargetAndAppliesNewReports(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "recovery.db"))
	if err := s.BindAgent(ctx, "agent", 1, [32]byte{1}); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"target", "other"} {
		if err := s.RegisterAssignedTarget(ctx, "project", target, "agent", 1); err != nil {
			t.Fatal(err)
		}
	}
	artifact, err := release.Compile(ctx, []byte("services: {web: {image: 'nginx@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'}}"), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PublishRelease(ctx, "project", "release", artifact); err != nil {
		t.Fatal(err)
	}
	binding := release.Binding{Target: "target", ConfigVersion: "v1", HealthTimeout: time.Minute}
	snap, err := artifact.Bind(binding)
	if err != nil {
		t.Fatal(err)
	}
	a := Admission{ID: "old", Project: "project", Caller: "owner", Key: "old", Target: "target", Release: "release"}
	if _, err := s.AdmitSnapshot(ctx, a, snap); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err := s.NextOffer(ctx, "agent", 1, now, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.enterRecovery(ctx); err != nil {
		t.Fatal(err)
	}
	view, err := s.RecoveryStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	attestation := "Checked Docker project and verified no deployment command is running"
	if err := s.ReleaseRestoredTarget(ctx, view.Epoch, "project", "target", "operator", attestation); !errors.Is(err, ErrRecovery) {
		t.Fatalf("released without inventory: %v", err)
	}
	item := deployment.Evidence{Execution: "old", Target: "target", Digest: snap.Digest(), Sequence: 1, State: deployment.Executing}
	if err := s.RecordInventory(ctx, view.Epoch, "agent", 1, []deployment.Evidence{item}, true); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseRestoredTarget(ctx, view.Epoch, "project", "target", "operator", attestation); !errors.Is(err, ErrTargetBusy) {
		t.Fatalf("released while active: %v", err)
	}
	item.Sequence, item.State = 3, deployment.Succeeded
	if err := s.RecordInventory(ctx, view.Epoch, "agent", 1, []deployment.Evidence{item}, true); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseRestoredTarget(ctx, view.Epoch, "project", "target", "operator", attestation); !errors.Is(err, ErrTargetBusy) {
		t.Fatalf("released with a still-valid historical offer: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE deployments SET expires_ms=? WHERE id='old'", now.Add(-time.Minute).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseRestoredTarget(ctx, view.Epoch, "project", "target", "operator", attestation); err != nil {
		t.Fatal(err)
	}
	archived, err := s.Get(ctx, "old")
	if err != nil || archived.State != deployment.Abandoned || archived.LastSuccess != "" {
		t.Fatalf("historical execution: %+v %v", archived, err)
	}
	a.ID, a.Key = "new", "new"
	if _, err := s.AdmitSnapshot(ctx, a, snap); err != nil {
		t.Fatal(err)
	}
	binding.Target = "other"
	other, err := artifact.Bind(binding)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdmitSnapshot(ctx, Admission{ID: "blocked", Project: "project", Caller: "owner", Key: "blocked", Target: "other", Release: "release"}, other); !errors.Is(err, ErrRecovery) {
		t.Fatalf("unrelated target was unfrozen: %v", err)
	}
	offer, err := s.NextOffer(ctx, "agent", 1, now, time.Minute)
	if err != nil || offer.ID != "new" {
		t.Fatalf("new deployment could not dispatch: %+v %v", offer, err)
	}
	late := Event{ID: "late", Execution: "old", Agent: "agent", Generation: 1, Sequence: 3, State: deployment.Succeeded}
	if err := s.ApplyEvent(ctx, late); err != nil {
		t.Fatal(err)
	}
	for i, state := range []deployment.State{deployment.Executing, deployment.Verifying, deployment.Succeeded} {
		event := Event{ID: string(state), Execution: offer.Execution, Agent: "agent", Generation: 1, Sequence: int64(i + 1), State: state}
		if err := s.ApplyEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	fresh, err := s.Get(ctx, "new")
	if err != nil || fresh.State != deployment.Succeeded || fresh.LastSuccess != "release" {
		t.Fatalf("new result: %+v %v", fresh, err)
	}
	if _, err := s.NextOffer(ctx, "agent", 1, now, time.Minute); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unexpected offer: %v", err)
	}
}
