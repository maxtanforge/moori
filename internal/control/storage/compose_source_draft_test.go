package storage

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/core/nodeops"
)

func TestComposeSourceDraftIsSealedUntilDispatch(t *testing.T) {
	ctx := t.Context()
	store := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	session, err := store.InitializeAdmin(ctx, "admin", "initial password", time.Hour)
	must(err)
	must(store.BindAgent(ctx, "edge", 1, [32]byte{1}))
	now := time.Now()
	must(store.RecordNodeOperationCapability(ctx, "edge", 1, now))
	const draft = "# exact private draft\nservices: {web: {image: nginx}}"
	req := nodeops.Request{Action: nodeops.ComposeSourceSave, Target: "app", ComposeSource: &nodeops.ComposeSourceEdit{
		Revision: strings.Repeat("a", 64), Path: "app/compose.yaml", Content: draft,
	}}
	queued, err := store.QueueNodeOperation(ctx, session, "edge", 1, req, now)
	must(err)
	var raw []byte
	must(store.db.QueryRowContext(ctx, "SELECT request FROM node_operations WHERE id=?", queued.ID).Scan(&raw))
	if bytes.Contains(raw, []byte("exact private draft")) {
		t.Fatal("Compose draft was stored as plaintext")
	}
	claimed, _, err := store.NextNodeOperation(ctx, "edge", 1, now)
	must(err)
	if claimed.Operation != queued.ID || claimed.ComposeSource == nil || claimed.ComposeSource.Content != draft {
		t.Fatal("the authorized node did not receive the exact draft")
	}

	other, err := store.QueueNodeOperation(ctx, session, "edge", 1, req, now)
	must(err)
	must(store.CompleteNodeOperation(ctx, "edge", 1, queued.ID, nodeops.Succeeded, "",
		&nodeops.Result{ComposeSource: &nodeops.ComposeSourceResult{Revision: strings.Repeat("b", 64)}}, now))
	store.composeDraftKey = [32]byte{9}
	if _, _, err := store.NextNodeOperation(ctx, "edge", 1, now); err == nil {
		t.Fatal("an expired encryption key dispatched an earlier source edit")
	}
	op, err := store.GetNodeOperation(ctx, session, "edge", other.ID, now)
	must(err)
	if op.State != nodeops.Failed {
		t.Fatal("unable to read a queued draft after restart without blocking later operations")
	}
}

func TestComposeSourceCreateDispatchAndRetry(t *testing.T) {
	ctx := t.Context()
	store := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	session, err := store.InitializeAdmin(ctx, "admin", "initial password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BindAgent(ctx, "edge", 1, [32]byte{1}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := store.RecordNodeOperationCapability(ctx, "edge", 1, now); err != nil {
		t.Fatal(err)
	}
	req := nodeops.Request{Action: "compose.source-create", Target: "example", ComposeSource: &nodeops.ComposeSourceEdit{
		Path: "compose.yaml", Content: "# private draft\nservices: {app: {image: nginx}}",
	}}
	queue := func() nodeops.Operation {
		t.Helper()
		op, err := store.QueueNodeOperation(ctx, session, "edge", 1, req, now)
		if err != nil {
			t.Fatal(err)
		}
		return op
	}
	op := queue()
	if retry := queue(); retry.ID != op.ID {
		t.Fatal("a retry queued a second create")
	}
	original := req.ComposeSource.Content
	req.ComposeSource.Content += "\n"
	if _, err := store.QueueNodeOperation(ctx, session, "edge", 1, req, now); err != ErrTargetBusy {
		t.Fatalf("conflicting draft: %v", err)
	}
	req.ComposeSource.Content = original
	stored := func() string {
		t.Helper()
		var raw string
		if err := store.db.QueryRowContext(ctx, "SELECT request FROM node_operations WHERE id=?", op.ID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	if raw := stored(); !strings.Contains(raw, "sealed_compose_draft") || strings.Contains(raw, "private draft") {
		t.Fatal("create draft was not sealed")
	}
	claimed, _, err := store.NextNodeOperation(ctx, "edge", 1, now)
	if err != nil || claimed.Operation != op.ID || claimed.Target != req.Target || claimed.ComposeSource == nil || *claimed.ComposeSource != *req.ComposeSource {
		t.Fatalf("incorrect claim: %v", err)
	}
	if retry := queue(); retry.ID != op.ID || retry.State != nodeops.Running {
		t.Fatal("running create was queued twice")
	}
	if err := store.CompleteNodeOperation(ctx, "edge", 1, op.ID, nodeops.Succeeded, "", &nodeops.Result{}, now); err != nil {
		t.Fatal(err)
	}
	if stored() != "{}" {
		t.Fatal("completed create retained its draft")
	}
}
