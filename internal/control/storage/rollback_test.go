package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/core/deployment"
)

func TestRollbackCopiesSuccessfulSnapshotAndQueuesDirectly(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	if err := s.RegisterTarget(ctx, "project", "target"); err != nil {
		t.Fatal(err)
	}
	if err := s.AssignTarget(ctx, "target", "agent", 1); err != nil {
		t.Fatal(err)
	}
	old := testSnapshot(t, s, "historical-v1")
	id, err := s.AdmitSnapshot(ctx, Admission{ID: "source", Project: "project", Caller: "owner", Key: "source", Target: "target", Release: "release"}, old)
	if err != nil {
		t.Fatal(err)
	}
	request := RollbackRequest{ID: "rollback", Source: id, Key: "operation", Operator: "owner", Note: "roll back release"}
	if _, err := s.RequestRollback(ctx, request); !errors.Is(err, ErrState) {
		t.Fatalf("queued deployment used as rollback source: %v", err)
	}
	if _, err := s.NextOffer(ctx, "agent", 1, time.Now(), time.Minute); err != nil {
		t.Fatal(err)
	}
	for i, state := range []deployment.State{deployment.Executing, deployment.Verifying, deployment.Succeeded} {
		e := Event{ID: string(state), Execution: id, Agent: "agent", Generation: 1, Sequence: int64(i + 1), State: state}
		if err := s.ApplyEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	for _, newID := range []string{"rollback", "retry"} {
		request.ID = newID
		if got, err := s.RequestRollback(ctx, request); err != nil || got != "rollback" {
			t.Fatalf("rollback admission: %q %v", got, err)
		}
	}
	copied, err := s.ExecutionSnapshot(ctx, "rollback")
	if err != nil || copied != old.JSON() {
		t.Fatalf("rollback snapshot changed: %v", err)
	}
	view, err := s.Get(ctx, "rollback")
	if err != nil || view.RollbackOf != id || view.State != deployment.Queued || view.Digest != old.Digest() {
		t.Fatalf("rollback view: %+v %v", view, err)
	}
	original, err := s.Get(ctx, id)
	if err != nil || original.State != deployment.Succeeded {
		t.Fatalf("source altered: %+v %v", original, err)
	}
	request.Note = "different input"
	if _, err := s.RequestRollback(ctx, request); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed rollback replay accepted: %v", err)
	}
}
