package storage

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/core/deployment"
	"github.com/maxtanforge/moori/internal/core/release"
)

func TestComposeMaintenanceQueuesAndRejectsStaleRequests(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	admin, err := s.InitializeAdmin(ctx, "admin", "initial password", time.Hour)
	must(err)
	must(s.RegisterTarget(ctx, "project", "target"))
	_, _, err = s.IssueAgentCredential(ctx, "agent", time.Hour)
	must(err)
	must(s.AssignTarget(ctx, "target", "agent", 1))
	snapshot := testSnapshot(t, s, "initial")
	_, err = s.AdmitSnapshot(ctx, Admission{ID: "source", Project: "project", Caller: "operator", Key: "source", Target: "target", Release: "release"}, snapshot)
	must(err)
	_, err = s.NextOffer(ctx, "agent", 1, time.Now(), time.Minute)
	must(err)
	for i, state := range []deployment.State{deployment.Executing, deployment.Verifying, deployment.Succeeded} {
		must(s.ApplyEvent(ctx, Event{ID: string(state), Execution: "source", Agent: "agent", Generation: 1, Sequence: int64(i + 1), State: state}))
	}
	source, err := s.FindManagedCompose(ctx, admin, "agent", release.ComposeProjectName("target"))
	must(err)
	if source.Deployment != "source" || source.Node.Agent != "agent" {
		t.Fatalf("wrong source: %+v", source)
	}
	edit := ComposeMaintenanceInput{Compose: "services:\n  web:\n    image: nginx@sha256:" + strings.Repeat("a", 64) +
		"\n    restart: always\n    labels: {team: ops}\n    environment: {MODE: maintenance}\n    ports: ['8080:80']" +
		"\n    volumes: ['data:/data']\n    networks: [ops]\nvolumes: {data: {}}\nnetworks: {ops: {}}\n"}
	redeploy := ComposeMaintenanceInput{ForceRecreate: true}
	recreatePreview, err := s.PreviewComposeMaintenance(ctx, admin, "project", "source", redeploy)
	must(err)
	preview, err := s.PreviewComposeMaintenance(ctx, admin, "project", "source", edit)
	must(err)
	if preview.SnapshotDigest == recreatePreview.SnapshotDigest ||
		!strings.Contains(preview.Compose, `"restart":"always"`) {
		t.Fatalf("invalid preview: %+v", preview)
	}
	_, err = s.SubmitComposeMaintenance(ctx, admin, "project", "source", "invalid", "invalid", edit)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("unpreviewed submission: %v", err)
	}
	edit.Node, edit.SnapshotDigest = preview.Node, preview.SnapshotDigest
	wrongNode := edit
	wrongNode.Node.Generation++
	_, err = s.SubmitComposeMaintenance(ctx, admin, "project", "source", "wrong", "wrong", wrongNode)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("different node generation: %v", err)
	}
	for _, id := range []string{"edited", "retry"} {
		got, err := s.SubmitComposeMaintenance(ctx, admin, "project", "source", id, "edit-key", edit)
		if err != nil || got != "edited" {
			t.Fatalf("expected idempotent deployment, got %q: %v", got, err)
		}
	}
	view, err := s.Get(ctx, "edited")
	must(err)
	if view.State != deployment.Queued || view.Digest != preview.SnapshotDigest {
		t.Fatalf("deployment snapshot or queue state changed: %+v", view)
	}
	redeploy.Node, redeploy.SnapshotDigest = recreatePreview.Node, recreatePreview.SnapshotDigest
	_, err = s.SubmitComposeMaintenance(ctx, admin, "project", "source", "busy", "busy", redeploy)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("parallel apply against the same source: %v", err)
	}
	if _, err := s.RequestHumanCancel(ctx, admin, "project", "edited", "cancel edit"); err != nil {
		t.Fatal(err)
	}
	_, err = s.SubmitComposeMaintenance(ctx, admin, "project", "source", "redeployed", "redeploy-key", redeploy)
	must(err)
	document, err := s.ExecutionSnapshot(ctx, "redeployed")
	must(err)
	verified, err := release.Verify(ctx, document, recreatePreview.SnapshotDigest)
	must(err)
	if !verified.ForceRecreate || verified.Compose != recreatePreview.Compose {
		t.Fatalf("invalid redeployment payload: %+v", verified)
	}
}
