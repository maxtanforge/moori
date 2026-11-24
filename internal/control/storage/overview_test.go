package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/core/release"
)

// The dashboard summary must count in-flight work and
// window transitions from the audit trail.
func TestPlatformDeploymentSummary(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.RegisterTarget(ctx, "project", "target"); err != nil {
		t.Fatal(err)
	}
	admin, err := s.InitializeAdmin(ctx, "admin", "initial test password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := release.Compile(ctx, []byte("services: {api: {image: 'nginx@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'}}"), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PublishRelease(ctx, "project", "release", artifact); err != nil {
		t.Fatal(err)
	}
	snapshot, err := artifact.Bind(release.Binding{Target: "target", ConfigVersion: "test-v1", HealthTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdmitSnapshot(ctx, Admission{ID: "deploy", Project: "project", Caller: "requester", Key: "key", Target: "target", Release: "release"}, snapshot); err != nil {
		t.Fatal(err)
	}
	summary, err := s.PlatformDeploymentSummary(ctx, admin, time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if summary.Active != 1 || summary.FailedWindow != 0 || summary.SucceededWindow != 0 {
		t.Fatalf("queued summary = %+v", summary)
	}
	// Audit-window counts are driven directly: reaching succeeded/failed through
	// the dispatch machinery needs a live Agent, which unit tests do not have.
	past := time.Now().Add(-48 * time.Hour).UTC().Format(time.DateTime)
	for _, tc := range []struct{ state, at string }{
		{"succeeded", time.Now().UTC().Format(time.DateTime)},
		{"failed", time.Now().UTC().Format(time.DateTime)},
		{"failed", time.Now().UTC().Format(time.DateTime)},
		{"failed", past},
	} {
		if _, err := s.db.ExecContext(ctx, "INSERT INTO deployment_audit(deployment_id,state,created_at) SELECT id,?,? FROM deployments", tc.state, tc.at); err != nil {
			t.Fatal(err)
		}
	}
	summary, err = s.PlatformDeploymentSummary(ctx, admin, time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if summary.SucceededWindow != 1 || summary.FailedWindow != 2 {
		t.Fatalf("window summary = %+v", summary)
	}
	if summary.Active != 1 {
		t.Fatalf("summary lost state counts = %+v", summary)
	}
	// Non-admin sessions are denied exactly like the other platform views.
	if _, err := s.CreateHumanUser(ctx, "member", "member test password"); err != nil {
		t.Fatal(err)
	}
	token, err := s.LoginHuman(ctx, "member", "member test password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PlatformDeploymentSummary(ctx, token, time.Now()); err == nil {
		t.Fatal("non-admin session read the platform summary")
	}
}
