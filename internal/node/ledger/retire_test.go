package ledger

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/core/deployment"
)

func TestRetireInterruptedExecutionPreservesEvidenceAndReleasesTarget(t *testing.T) {
	ctx := context.Background()
	l, err := Open(ctx, filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	now := time.Now()
	original := Request{Execution: "old", Target: "target", Digest: "digest", Expires: now.Add(time.Hour)}
	if _, err := l.Accept(ctx, original, now); err != nil {
		t.Fatal(err)
	}
	if err := l.Record(ctx, "old", deployment.Executing); err != nil {
		t.Fatal(err)
	}
	if err := l.RetireInterrupted(ctx, "old", "operator", "Docker processes inspected"); !errors.Is(err, ErrState) {
		t.Fatalf("retired an executing task: %v", err)
	}
	if err := l.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	if err := l.RetireInterrupted(ctx, "old", "operator", "Docker processes inspected"); err != nil {
		t.Fatal(err)
	}
	if accepted, err := l.Accept(ctx, original, now); err != nil || accepted {
		t.Fatalf("old task accepted again: %v %v", accepted, err)
	}
	next := original
	next.Execution = "new"
	if accepted, err := l.Accept(ctx, next, now); err != nil || !accepted {
		t.Fatalf("target lock not released: %v %v", accepted, err)
	}
	events, err := l.Pending(ctx, 10)
	if err != nil || len(events) != 3 || events[2].State != deployment.Abandoned {
		t.Fatalf("incomplete retirement evidence: %+v %v", events, err)
	}
}
