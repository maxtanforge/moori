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

func TestCancelRequestDoesNotRewriteSuccess(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "db"))
	if err := s.RegisterTarget(ctx, "project", "target"); err != nil {
		t.Fatal(err)
	}
	if err := s.AssignTarget(ctx, "target", "agent", 1); err != nil {
		t.Fatal(err)
	}
	snap := testSnapshot(t, s, "v1")
	for _, id := range []string{"first", "queued"} {
		if _, err := s.AdmitSnapshot(ctx, Admission{ID: id, Project: "project", Caller: "owner", Target: "target", Release: "release", Key: id}, snap); err != nil {
			t.Fatal(err)
		}
	}
	if result, err := s.RequestCancel(ctx, "queued", "operator", "test"); err != nil || result.State != deployment.Canceled {
		t.Fatalf("queue cancellation: %+v %v", result, err)
	}
	if _, err := s.NextOffer(ctx, "agent", 1, time.Now(), time.Minute); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		result, err := s.RequestCancel(ctx, "first", "operator", "test")
		if err != nil || result.Decision != deployment.RequestAgentCancel {
			t.Fatalf("running cancellation: %+v %v", result, err)
		}
	}
	if _, err := s.NextCancel(ctx, "agent", 2); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("old generation received cancel: %v", err)
	}
	for range 2 {
		if id, err := s.NextCancel(ctx, "agent", 1); err != nil || id != "first" {
			t.Fatalf("pending cancel: %q %v", id, err)
		}
	}
	if _, err := s.NextOffer(ctx, "agent", 1, time.Now(), time.Minute); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("execution offered after cancel: %v", err)
	}
	for i, state := range []deployment.State{deployment.Executing, deployment.Verifying, deployment.Succeeded} {
		if err := s.ApplyEvent(ctx, Event{ID: string(state), Execution: "first", Agent: "agent", Generation: 1, Sequence: int64(i + 1), State: state}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AcknowledgeCancel(ctx, "first", "agent", 1, true); err != nil {
		t.Fatal(err)
	}
	result, err := s.RequestCancel(ctx, "first", "operator", "too late")
	if err != nil || result.State != deployment.Succeeded || result.Decision != deployment.CancelTooLate {
		t.Fatalf("late cancellation: %+v %v", result, err)
	}
}
