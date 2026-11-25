package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/core/deployment"
	"github.com/maxtanforge/moori/internal/core/release"
)

func TestReconciliationReportRetainsTargetButPermitsOtherWork(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	for _, target := range []string{"target", "other"} {
		if err := s.RegisterTarget(ctx, "project", target); err != nil {
			t.Fatal(err)
		}
		if err := s.AssignTarget(ctx, target, "agent", 1); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := testSnapshot(t, s, "v1")
	for _, id := range []string{"first", "second"} {
		if _, err := s.AdmitSnapshot(ctx, Admission{ID: id, Project: "project", Caller: "ci", Key: id, Target: "target", Release: "release"}, snapshot); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	if _, err := s.NextOffer(ctx, "agent", 1, now, time.Minute); err != nil {
		t.Fatal(err)
	}
	progress := Event{ID: "progress", Execution: "first", Agent: "agent", Generation: 1, Sequence: 1, State: deployment.Executing}
	if err := s.ApplyEvent(ctx, progress); err != nil {
		t.Fatal(err)
	}
	// A local control action may already have entered reconciliation.
	if err := s.MarkReconciling(ctx, "first", 1); err != nil {
		t.Fatal(err)
	}
	interrupted := Event{ID: "interrupt", Execution: "first", Agent: "agent", Generation: 1, Sequence: 2, State: deployment.Reconciling}
	for range 2 {
		if err := s.ApplyEvent(ctx, interrupted); err != nil {
			t.Fatal(err)
		}
	}
	r, err := release.Compile(ctx, []byte("services: {web: {image: 'nginx@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'}}"), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	other, err := r.Bind(release.Binding{Target: "other", ConfigVersion: "v1", HealthTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdmitSnapshot(ctx, Admission{ID: "other-work", Project: "project", Caller: "ci", Key: "other", Target: "other", Release: "release"}, other); err != nil {
		t.Fatal(err)
	}
	offer, err := s.NextOffer(ctx, "agent", 1, now, time.Minute)
	if err != nil || offer.ID != "other-work" {
		t.Fatalf("expected only unrelated target, got %+v %v", offer, err)
	}
}
