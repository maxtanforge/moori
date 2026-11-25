package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestClaimReloadsExactOffer(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")
	s := openTestStore(t, path)
	if err := s.RegisterTarget(ctx, "project", "target"); err != nil {
		t.Fatal(err)
	}
	snapshot := testSnapshot(t, s, "v1")
	id, err := s.AdmitSnapshot(ctx, Admission{ID: "deploy", Project: "project", Caller: "ci", Key: "request", Target: "target", Release: "release"}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().UTC().Truncate(time.Millisecond).Add(time.Minute)
	if err := s.Claim(ctx, id, "attempt", "agent", 2, deadline); err != nil {
		t.Fatal(err)
	}
	if err := s.Claim(ctx, id, "attempt", "agent", 2, deadline); err != nil {
		t.Fatalf("retry claim: %v", err)
	}
	if err := s.Claim(ctx, id, "attempt", "agent", 2, deadline.Add(time.Minute)); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed deadline: %v", err)
	}
	s = openTestStore(t, path)
	offer, err := s.Offer(ctx, "attempt")
	if err != nil || offer.ID != id || offer.Target != "target" || offer.Execution != "attempt" ||
		offer.Agent != "agent" || offer.Generation != 2 || !offer.Expires.Equal(deadline) ||
		offer.Digest != snapshot.Digest() || offer.Document != snapshot.JSON() {
		t.Fatalf("offer=%+v err=%v", offer, err)
	}
}
