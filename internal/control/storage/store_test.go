package storage

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/core/deployment"
	"github.com/maxtanforge/moori/internal/core/release"
)

func testSnapshot(t *testing.T, s *Store, version string) release.Snapshot {
	t.Helper()
	ctx := context.Background()
	r, err := release.Compile(ctx, []byte("services: {web: {image: 'nginx@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'}}"), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PublishRelease(ctx, "project", "release", r); err != nil {
		t.Fatal(err)
	}
	snapshot, err := r.Bind(release.Binding{Target: "target", ConfigVersion: version, HealthTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func openTestStore(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestAdmissionIsIdempotentAcrossConnections(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")
	a, b := openTestStore(t, path), openTestStore(t, path)
	if err := a.RegisterTarget(ctx, "project", "target"); err != nil {
		t.Fatal(err)
	}
	snapshot := testSnapshot(t, a, "v1")
	var result [2]string
	var wg sync.WaitGroup
	for i, s := range []*Store{a, b} {
		wg.Go(func() {
			got, err := s.AdmitSnapshot(ctx, Admission{ID: []string{"first", "second"}[i], Project: "project", Caller: "ci", Key: "request", Target: "target", Release: "release"}, snapshot)
			if err != nil {
				t.Error(err)
				return
			}
			result[i] = got
		})
	}
	wg.Wait()
	if result[0] == "" || result[0] != result[1] {
		t.Fatalf("duplicate did not return the original result: %v", result)
	}
	var count int
	if err := a.db.QueryRow("SELECT count(*) FROM deployments").Scan(&count); err != nil || count != 1 {
		t.Fatalf("rows=%d err=%v", count, err)
	}
	_, err := b.AdmitSnapshot(ctx, Admission{ID: "third", Project: "project", Caller: "ci", Key: "request", Target: "target", Release: "release"}, testSnapshot(t, b, "v2"))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("want input conflict, got %v", err)
	}
}

func TestTargetRemainsLockedAndCompletionRollsBack(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	if err := s.RegisterTarget(ctx, "project", "target"); err != nil {
		t.Fatal(err)
	}
	snapshot := testSnapshot(t, s, "v1")
	for _, id := range []string{"one", "two"} {
		if _, err := s.AdmitSnapshot(ctx, Admission{ID: id, Project: "project", Caller: "ci", Key: id, Target: "target", Release: "release"}, snapshot); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Claim(ctx, "one", "exec1", "agent", 1, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.Claim(ctx, "two", "exec2", "agent", 1, time.Now().Add(time.Minute)); !errors.Is(err, ErrTargetBusy) {
		t.Fatalf("claim while busy: %v", err)
	}
	if err := s.MarkReconciling(ctx, "exec1", 1); err != nil {
		t.Fatal(err)
	}
	if err := s.Claim(ctx, "two", "exec2", "agent", 1, time.Now().Add(time.Minute)); !errors.Is(err, ErrTargetBusy) {
		t.Fatalf("claim while reconciling: %v", err)
	}
	event := Event{ID: "event1", Execution: "exec1", Agent: "agent", Generation: 1, Sequence: 1, State: deployment.Succeeded}
	if _, err := s.db.Exec("CREATE TRIGGER fail_outbox BEFORE INSERT ON outbox BEGIN SELECT RAISE(ABORT,'test outbox failure'); END"); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyEvent(ctx, event); err == nil {
		t.Fatal("expected outbox failure")
	}
	view, err := s.Get(ctx, "one")
	if err != nil || view.State != deployment.Reconciling || view.LastSuccess != "" {
		t.Fatalf("partial commit: %+v %v", view, err)
	}
	if err := s.Claim(ctx, "two", "exec2", "agent", 1, time.Now().Add(time.Minute)); !errors.Is(err, ErrTargetBusy) {
		t.Fatalf("claim after rollback: %v", err)
	}
	if _, err := s.db.Exec("DROP TRIGGER fail_outbox"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.ApplyEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	view, err = s.Get(ctx, "one")
	if err != nil || view.State != deployment.Succeeded || view.LastSuccess != "release" {
		t.Fatalf("success: %+v %v", view, err)
	}
	changed := event
	changed.State = deployment.Failed
	if err := s.ApplyEvent(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed event: %v", err)
	}
	if err := s.Claim(ctx, "two", "exec2", "agent", 1, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
}
