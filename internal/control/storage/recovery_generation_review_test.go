package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/maxtanforge/moori/internal/core/deployment"
)

func TestRecoveryReplacementRotationKeepsUnknownActiveEvidence(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.BindAgent(ctx, "original", 1, [32]byte{1}))
	must(s.BindAgent(ctx, "replacement", 1, [32]byte{2}))
	must(s.RegisterAssignedTarget(ctx, "project", "target", "original", 1))
	must(s.enterRecovery(ctx))
	epoch, err := s.RecoveryEpoch(ctx)
	must(err)
	must(s.RecordInventory(ctx, epoch, "original", 1, nil, true))
	must(s.ReleaseRestoredTarget(ctx, epoch, "project", "target", "operator", "Docker inspected"))
	must(s.ApplyEvent(ctx, Event{ID: "unknown-active", Execution: "unknown", Agent: "replacement", Generation: 1, Sequence: 1, State: deployment.Executing}))
	must(s.BindAgent(ctx, "replacement", 2, [32]byte{3}))
	if err := s.ReassignTarget(ctx, "project", "target", "replacement", 2); !errors.Is(err, ErrTargetBusy) {
		t.Fatalf("rotation bypassed unknown active evidence: got %v, want ErrTargetBusy", err)
	}
	// Reusing an execution ID in the new identity cannot settle the old work.
	must(s.ApplyEvent(ctx, Event{ID: "new-generation-done", Execution: "unknown", Agent: "replacement", Generation: 2, Sequence: 2, State: deployment.Succeeded}))
	if err := s.ReassignTarget(ctx, "project", "target", "replacement", 2); !errors.Is(err, ErrTargetBusy) {
		t.Fatalf("new generation terminal event cleared old work: %v", err)
	}
}
