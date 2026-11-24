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

func TestNextOfferRetriesOriginalAndIsolatesAssignments(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "queue.db"))
	if err := s.RegisterTarget(ctx, "project", "target"); err != nil {
		t.Fatal(err)
	}
	if err := s.AssignTarget(ctx, "target", "agent", 2); err != nil {
		t.Fatal(err)
	}
	snapshot := testSnapshot(t, s, "v1")
	for _, id := range []string{"first", "second"} {
		if _, err := s.AdmitSnapshot(ctx, Admission{ID: id, Project: "project", Caller: "ci", Key: id, Target: "target", Release: "release"}, snapshot); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := s.NextOffer(ctx, "agent", 1, now, time.Minute); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("old generation found work: %v", err)
	}
	first, err := s.NextOffer(ctx, "agent", 2, now, time.Minute)
	if err != nil || first.ID != "first" {
		t.Fatalf("first offer: %+v %v", first, err)
	}
	duplicate, err := s.NextOffer(ctx, "agent", 2, now.Add(time.Second), time.Minute)
	if err != nil || first != duplicate {
		t.Fatalf("offer changed on retry: %+v / %+v, %v", first, duplicate, err)
	}
	if err := s.AssignTarget(ctx, "target", "other", 1); !errors.Is(err, ErrTargetBusy) {
		t.Fatalf("reassigned active target: %v", err)
	}
	if _, err := s.NextOffer(ctx, "agent", 2, now.Add(time.Minute), time.Minute); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expired offer replayed or lock released: %v", err)
	}
	view, err := s.Get(ctx, "first")
	if err != nil || view.State != deployment.Reconciling {
		t.Fatalf("expired offer was not reconciled: %+v %v", view, err)
	}
}
