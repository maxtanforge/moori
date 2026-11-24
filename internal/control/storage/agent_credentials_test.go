package storage

import (
	"context"
	"crypto/sha256"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAgentCredentialLifecycle(t *testing.T) {
	ctx := context.Background()
	s, e := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	token, g, e := s.IssueAgentCredential(ctx, "node", time.Hour)
	if e != nil || g != 1 {
		t.Fatalf("issue %d %v", g, e)
	}
	id, got, e := s.AuthenticateAgentCredential(ctx, token)
	if e != nil || id != "node" || got != g {
		t.Fatalf("authenticate %s %d %v", id, got, e)
	}
	next, g, e := s.IssueAgentCredential(ctx, "node", time.Hour)
	if e != nil || g != 2 {
		t.Fatalf("rotate %d %v", g, e)
	}
	if _, _, e = s.AuthenticateAgentCredential(ctx, token); !errors.Is(e, ErrForbidden) {
		t.Fatal("old credential accepted", e)
	}
	if e = s.ValidateAgent(ctx, "node", 1); e == nil {
		t.Fatal("old session accepted")
	}
	if e = s.RevokeAgentCredential(ctx, "node"); e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.AuthenticateAgentCredential(ctx, next); !errors.Is(e, ErrForbidden) {
		t.Fatal("revoked accepted", e)
	}
	if e = s.ValidateAgent(ctx, "node", 2); e == nil {
		t.Fatal("revoked session accepted")
	}
	expired, g, e := s.IssueAgentCredential(ctx, "node", time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.db.Exec("UPDATE agent_credentials SET expires_ms=0 WHERE agent='node'"); e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.AuthenticateAgentCredential(ctx, expired); !errors.Is(e, ErrForbidden) {
		t.Fatal("expired accepted", e)
	}
	if e = s.ValidateAgent(ctx, "node", g); e == nil {
		t.Fatal("expired session accepted")
	}
}
func TestExistingPrefixedAgentTokenKeepsItsIdentity(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "db"))
	token, _, err := s.IssueAgentCredential(ctx, "node", time.Hour)
	if err != nil || len(token) != 43 {
		t.Fatal("expected a 43-character Agent token", err)
	}
	old := "moori_agent_" + token
	hash := sha256.Sum256([]byte(old))
	if _, err := s.db.Exec("UPDATE agent_credentials SET token_hash=? WHERE agent='node'", hash[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE agent_certificates SET fingerprint=? WHERE agent='node'", hash[:]); err != nil {
		t.Fatal(err)
	}
	if id, _, err := s.AuthenticateAgentCredential(ctx, old); err != nil || id != "node" {
		t.Fatal("existing Agent could not reconnect", err)
	}
	if _, _, err := s.AuthenticateAgentCredential(ctx, token); !errors.Is(err, ErrForbidden) {
		t.Fatal("modified credential accepted")
	}
}

func TestAgentCredentialRejectsNoncanonicalEncoding(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "db"))
	token, _, err := s.IssueAgentCredential(ctx, "node", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// A trailing newline is ignored by encoding/base64, but cannot be a credential.
	for _, bad := range []string{token + "\n", token + strings.Repeat("A", 1<<20)} {
		hash := sha256.Sum256([]byte(bad))
		if _, err := s.db.Exec("UPDATE agent_credentials SET token_hash=? WHERE agent='node'", hash[:]); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec("UPDATE agent_certificates SET fingerprint=? WHERE agent='node'", hash[:]); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.AuthenticateAgentCredential(ctx, bad); !errors.Is(err, ErrForbidden) {
			t.Fatalf("noncanonical credential accepted: %v", err)
		}
	}
}
