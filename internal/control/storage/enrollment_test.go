package storage

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestEnrollmentRetryDoesNotIssueSecondIdentity(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	token, err := s.IssueEnrollment(ctx, "host-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	csr := [32]byte{4}
	var issued int
	var lineage string
	sign := func(id string, gen int64, tag string) ([]byte, error) {
		issued++
		lineage = tag
		if id != "host-1" || gen != 1 || tag == "" {
			t.Fatalf("incorrect issuance identity: %s %d", id, gen)
		}
		return []byte("certificate"), nil
	}
	for range 2 {
		der, err := s.CompleteEnrollment(ctx, token, csr, now, sign)
		if err != nil || !bytes.Equal(der, []byte("certificate")) {
			t.Fatalf("enrollment: %v", err)
		}
	}
	if issued != 1 {
		t.Fatalf("issued %d times", issued)
	}
	if _, err := s.CompleteEnrollment(ctx, token, [32]byte{5}, now, sign); !errors.Is(err, ErrForbidden) {
		t.Fatalf("different CSR reused token: %v", err)
	}
	if _, err := s.CompleteEnrollment(ctx, token, csr, now.Add(2*time.Minute), sign); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expired replay was accepted: %v", err)
	}
	if err := s.ValidateManagedAgent(ctx, "host-1", 1, lineage); err != nil {
		t.Fatal(err)
	}
	if err := s.BindAgent(ctx, "host-1", 2, [32]byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateManagedAgent(ctx, "host-1", 1, lineage); !errors.Is(err, ErrForbidden) {
		t.Fatalf("old lineage was still valid: %v", err)
	}
}
