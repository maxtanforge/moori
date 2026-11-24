package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/core/release"
)

func TestCompiledSnapshotPersistsAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")
	s := openTestStore(t, path)
	if err := s.RegisterTarget(ctx, "project", "target"); err != nil {
		t.Fatal(err)
	}
	r, err := release.Compile(ctx, []byte("services: {web: {image: 'nginx@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'}}"), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := r.Bind(release.Binding{Target: "target", ConfigVersion: "v1", HealthTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PublishRelease(ctx, "project", "release", r); err != nil {
		t.Fatal(err)
	}
	a := Admission{ID: "deployment", Project: "project", Caller: "ci", Key: "request", Target: "target", Release: "release"}
	if _, err := s.AdmitSnapshot(ctx, a, snapshot); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, path)
	got, err := reopened.ExecutionSnapshot(ctx, a.ID)
	if err != nil || got != snapshot.JSON() {
		t.Fatalf("stored snapshot changed after restart: %q %v", got, err)
	}
}
