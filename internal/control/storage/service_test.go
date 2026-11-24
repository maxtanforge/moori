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

func TestServiceCredentialScopeAndRevocationAtDispatch(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	if err := s.RegisterTarget(ctx, "project", "target"); err != nil {
		t.Fatal(err)
	}
	if err := s.AssignTarget(ctx, "target", "agent", 1); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	token, err := s.IssueServiceCredential(ctx, "ci", "project", "target", now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	snap := testSnapshot(t, s, "v1")
	a := Admission{ID: "deploy", Project: "another", Key: "request", Target: "target", Release: "release"}
	if _, err := s.AdmitServiceSnapshot(ctx, token, a, snap); !errors.Is(err, ErrForbidden) {
		t.Fatalf("scope check: %v", err)
	}
	a.Project = "project"
	if _, err := s.AdmitServiceSnapshot(ctx, token, a, snap); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeServiceCredential(ctx, "ci"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdmitServiceSnapshot(ctx, token, a, snap); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoked credential replay: %v", err)
	}
	if _, err := s.NextOffer(ctx, "agent", 1, now, time.Minute); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("revoked request was dispatched: %v", err)
	}
	item, err := s.Get(ctx, a.ID)
	if err != nil || item.State != deployment.Failed {
		t.Fatalf("revoked queue item: %+v %v", item, err)
	}
}

func TestCredentialExpiryStopsRetryOfUnconfirmedOffer(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	if err := s.RegisterTarget(ctx, "project", "target"); err != nil {
		t.Fatal(err)
	}
	if err := s.AssignTarget(ctx, "target", "agent", 1); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	expires := now.Add(time.Minute)
	token, err := s.IssueServiceCredential(ctx, "ci", "project", "target", expires)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := testSnapshot(t, s, "v1")
	a := Admission{ID: "deploy", Project: "project", Key: "request", Target: "target", Release: "release"}
	if _, err := s.AdmitServiceSnapshot(ctx, token, a, snapshot); err != nil {
		t.Fatal(err)
	}
	offer, err := s.NextOffer(ctx, "agent", 1, now, 5*time.Minute)
	if err != nil || offer.ID != a.ID {
		t.Fatalf("initial offer: %+v %v", offer, err)
	}
	if _, err := s.NextOffer(ctx, "agent", 1, expires, 5*time.Minute); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expired credential still retried: %v", err)
	}
	item, err := s.Get(ctx, a.ID)
	if err != nil || item.State != deployment.Reconciling {
		t.Fatalf("lost uncertainty: %+v %v", item, err)
	}
	if err := s.AssignTarget(ctx, "target", "agent", 2); !errors.Is(err, ErrTargetBusy) {
		t.Fatalf("uncertain target lock was released: %v", err)
	}
}
