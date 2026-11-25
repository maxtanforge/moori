package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/core/nodeops"
)

func TestNodeOperationNeverReoffersClaimedAction(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := store.CreateHumanUser(ctx, "admin", "test password")
	must(err)
	must(store.SetPlatformAdmin(ctx, "admin", true))
	token, err := store.LoginHuman(ctx, "admin", "test password", time.Hour)
	must(err)
	must(store.BindAgent(ctx, "node-1", 1, [32]byte{1}))
	now := time.Now()
	req := nodeops.Request{Action: nodeops.VolumeCreate, Name: "data"}
	if _, err := store.QueueNodeOperation(ctx, token, "node-1", 1, req, now); !errors.Is(err, ErrNodeOperationsUnavailable) {
		t.Fatalf("did not require capability: %v", err)
	}
	must(store.RecordNodeOperationCapability(ctx, "node-1", 1, now))
	op, err := store.QueueNodeOperation(ctx, token, "node-1", 1, req, now)
	must(err)
	first, _, err := store.NextNodeOperation(ctx, "node-1", 1, now)
	must(err)
	if first.Operation != op.ID {
		t.Fatalf("claimed %q want %q", first.Operation, op.ID)
	}
	if _, _, err := store.NextNodeOperation(ctx, "node-1", 1, now.Add(time.Second)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("operation offered twice: %v", err)
	}
	if _, err := store.RunningNodeOperation(ctx, "another-node", 1, op.ID, now); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("wrong peer reached in-flight operation: %v", err)
	}
	history, err := store.ListNodeOperationHistory(ctx, token, "node-1", "", now.Add(time.Minute))
	must(err)
	if len(history) != 1 || history[0].ID != op.ID || history[0].State != nodeops.Uncertain || history[0].Target != "data" {
		t.Fatalf("lost result history=%+v", history)
	}
	if _, _, err := store.NextNodeOperation(ctx, "node-1", 1, now.Add(2*time.Minute)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("operation replayed after timeout: %v", err)
	}
	must(store.CompleteNodeOperation(ctx, "node-1", 1, op.ID, nodeops.Succeeded, "", &nodeops.Result{}, now.Add(3*time.Minute)))
	must(store.CompleteNodeOperation(ctx, "node-1", 1, op.ID, nodeops.Failed, "duplicate report", nil, now.Add(4*time.Minute)))
	history, err = store.ListNodeOperationHistory(ctx, token, "node-1", op.ID, now.Add(4*time.Minute))
	must(err)
	if len(history) != 1 || history[0].State != nodeops.Succeeded || history[0].Error != "" {
		t.Fatalf("late receipt did not resolve timeout exactly once: %+v", history)
	}
}

func TestContainerCreateDeduplicatesPendingNameAndScrubsExpiredRequest(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := store.CreateHumanUser(ctx, "admin", "test password")
	must(err)
	must(store.SetPlatformAdmin(ctx, "admin", true))
	token, err := store.LoginHuman(ctx, "admin", "test password", time.Hour)
	must(err)
	must(store.EnsureLocalExecutor(ctx))
	now := time.Now()
	must(store.RecordNodeOperationCapability(ctx, LocalExecutorID, 1, now))
	req := nodeops.Request{Action: nodeops.ContainerCreate, Name: "web-test", Edit: &nodeops.ContainerEdit{Image: "nginx:alpine"}}
	first, err := store.QueueNodeOperation(ctx, token, LocalExecutorID, 1, req, now)
	must(err)
	second, err := store.QueueNodeOperation(ctx, token, LocalExecutorID, 1, req, now)
	must(err)
	if second.ID != first.ID {
		t.Fatalf("duplicate create queued twice: %q, %q", first.ID, second.ID)
	}
	req.Edit.Image = "nginx:latest"
	if _, err := store.QueueNodeOperation(ctx, token, LocalExecutorID, 1, req, now); !errors.Is(err, ErrTargetBusy) {
		t.Fatalf("conflicting create for same name: %v", err)
	}
	req.Name = "other-test"
	other, err := store.QueueNodeOperation(ctx, token, LocalExecutorID, 1, req, now)
	must(err)
	if other.ID == first.ID {
		t.Fatal("different name reused an operation")
	}
	if _, err := store.GetNodeOperation(ctx, token, LocalExecutorID, first.ID, now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var stored string
	must(store.db.QueryRowContext(ctx, "SELECT request FROM node_operations WHERE id=?", first.ID).Scan(&stored))
	if stored != "{}" {
		t.Fatal("expired create request was retained")
	}
}

func TestLongImagePullAndInterruptedClaim(t *testing.T) {
	ctx := t.Context()
	store := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	token, err := store.InitializeAdmin(ctx, "admin", "test password", 8*time.Hour)
	must(err)
	must(store.EnsureLocalExecutor(ctx))
	now := time.Now()
	req := nodeops.Request{Action: nodeops.RegistryPull, Image: "alpine:latest"}
	queue := func(at time.Time) nodeops.Operation {
		t.Helper()
		must(store.RecordNodeOperationCapability(ctx, LocalExecutorID, 1, at))
		op, err := store.QueueNodeOperation(ctx, token, LocalExecutorID, 1, req, at)
		must(err)
		return op
	}
	lost := queue(now)
	_, _, err = store.NextNodeOperation(ctx, LocalExecutorID, 1, now)
	must(err)
	later := now.Add(2 * time.Minute)
	first := queue(later)
	if first.ID == lost.ID {
		t.Fatal("lost claim still blocks retry")
	}
	claimed, _, err := store.NextNodeOperation(ctx, LocalExecutorID, 1, later)
	must(err)
	if claimed.Operation != first.ID {
		t.Fatal("retry did not receive the new claim")
	}
	_, err = store.RegistryOperation(ctx, SecretKey{}, LocalExecutorID, 1, first.ID, true)
	must(err)
	end := later.Add(90 * time.Minute)
	if op := queue(end); op.ID != first.ID || op.State != nodeops.Running {
		t.Fatalf("reopening the pull did not find the running operation: %+v", op)
	}
	must(store.CompleteNodeOperation(ctx, LocalExecutorID, 1, first.ID, nodeops.Succeeded, "", &nodeops.Result{Image: req.Image}, end))
	finished, err := store.GetNodeOperation(ctx, token, LocalExecutorID, first.ID, end)
	must(err)
	if finished.Result == nil || finished.Result.Image != req.Image || finished.State != nodeops.Succeeded {
		t.Fatal("lost the completed long pull result", finished)
	}
}

func TestNodeOperationHistoryPagination(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	token, err := store.InitializeAdmin(ctx, "admin", "test password", time.Hour)
	must(err)
	must(store.EnsureLocalExecutor(ctx))
	now := time.Now()
	must(store.RecordNodeOperationCapability(ctx, LocalExecutorID, 1, now))
	for _, name := range []string{"oldest", "middle", "newest"} {
		_, err := store.QueueNodeOperation(ctx, token, LocalExecutorID, 1, nodeops.Request{Action: nodeops.VolumeCreate, Name: name}, now)
		must(err)
	}
	first, err := store.PageNodeOperationHistory(ctx, token, LocalExecutorID, "", 2, now)
	must(err)
	if len(first.Items) != 2 || first.Items[0].Target != "newest" || first.Items[1].Target != "middle" || first.Next == "" {
		t.Fatalf("first page: %+v", first)
	}
	_, err = store.QueueNodeOperation(ctx, token, LocalExecutorID, 1, nodeops.Request{Action: nodeops.VolumeCreate, Name: "inserted"}, now.Add(time.Second))
	must(err)
	second, err := store.PageNodeOperationHistory(ctx, token, LocalExecutorID, first.Next, 2, now.Add(time.Second))
	must(err)
	if len(second.Items) != 1 || second.Items[0].Target != "oldest" || second.Next != "" {
		t.Fatalf("second page: %+v", second)
	}
	if _, err := store.PageNodeOperationHistory(ctx, token, LocalExecutorID, "invalid", 2, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid cursor error: %v", err)
	}
}
