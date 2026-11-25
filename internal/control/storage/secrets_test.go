package storage

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/core/deployment"
	"github.com/maxtanforge/moori/internal/core/release"
)

func TestExecutionSecretsPinEncryptedVersionsAndAgentIdentity(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	if err := s.BindAgent(ctx, "agent", 1, [32]byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterAssignedTarget(ctx, "team", "target", "agent", 1); err != nil {
		t.Fatal(err)
	}
	key := SecretKey{13}
	value := []byte("test-only confidential value")
	if err := s.PutSecret(ctx, key, "team", "target", "PASSWORD", "v1", value); err != nil {
		t.Fatal(err)
	}
	var stored []byte
	if err := s.db.QueryRow("SELECT ciphertext FROM target_secrets").Scan(&stored); err != nil || bytes.Contains(stored, value) {
		t.Fatalf("plaintext was stored or secret unavailable: %v", err)
	}
	if err := s.CheckSecretKey(ctx, SecretKey{11}); !errors.Is(err, ErrSecretKey) {
		t.Fatalf("wrong key accepted: %v", err)
	}
	original := SecretBindings{Secrets: map[string]string{"PASSWORD": "v1"}, Services: map[string][]string{"web": {"PASSWORD"}}}
	if err := s.SetTargetSecrets(ctx, "team", "target", "cfg1", original); err != nil {
		t.Fatal(err)
	}
	artifact, err := release.Compile(ctx, []byte("services: {web: {image: 'nginx@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'}}"), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PublishRelease(ctx, "team", "r1", artifact); err != nil {
		t.Fatal(err)
	}
	token, err := s.IssueServiceCredential(ctx, "ci", "team", "target", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdmitPublishedRelease(ctx, token, Admission{ID: "task", Project: "team", Target: "target", Key: "one", Release: "r1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSecret(ctx, key, "team", "target", "PASSWORD", "v2", []byte("replacement")); err != nil {
		t.Fatal(err)
	}
	original.Secrets["PASSWORD"] = "v2"
	if err := s.SetTargetSecrets(ctx, "team", "target", "cfg2", original); err != nil {
		t.Fatal(err)
	}
	offer, err := s.NextOffer(ctx, "agent", 1, time.Now(), time.Minute)
	if err != nil || bytes.Contains([]byte(offer.Document), value) {
		t.Fatalf("bad offer: %v", err)
	}
	if _, err := s.ExecutionSecrets(ctx, key, "agent", 2, offer.Execution, offer.Digest); !errors.Is(err, ErrForbidden) {
		t.Fatalf("wrong generation got secrets: %v", err)
	}
	bundle, err := s.ExecutionSecrets(ctx, key, "agent", 1, offer.Execution, offer.Digest)
	if err != nil || !bytes.Equal(bundle["PASSWORD"], value) {
		t.Fatalf("version not pinned: %v", err)
	}
	if err := s.RevokeSecret(ctx, "team", "target", "PASSWORD", "v1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExecutionSecrets(ctx, key, "agent", 1, offer.Execution, offer.Digest); !errors.Is(err, ErrSecretUnavailable) {
		t.Fatalf("revoked version could be fetched: %v", err)
	}
	for i, state := range []deployment.State{deployment.Executing, deployment.Verifying, deployment.Succeeded} {
		if err := s.ApplyEvent(ctx, Event{ID: string(state), Execution: offer.Execution, Agent: "agent", Generation: 1, Sequence: int64(i + 1), State: state}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.RequestRollback(ctx, RollbackRequest{ID: "rollback", Source: "task", Key: "rollback", Operator: "operator", Note: "restore old configuration"}); !errors.Is(err, ErrSecretUnavailable) {
		t.Fatalf("rollback accepted a revoked version: %v", err)
	}
}
