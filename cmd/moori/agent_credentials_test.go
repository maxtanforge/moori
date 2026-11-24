package main

import (
	"bytes"
	"context"
	"github.com/maxtanforge/moori/internal/control/storage"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentCredentialCommands(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "control.db")
	var out, diagnostics bytes.Buffer
	if err := agentCredentialCommand(ctx, "issue-agent-credential", []string{"-db", db, "-agent", "node-1"}, &out, &diagnostics); err != nil {
		t.Fatal(err)
	}
	token := strings.TrimSpace(out.String())
	if len(token) != 43 {
		t.Fatal("missing credential")
	}
	s, err := storage.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id, generation, err := s.AuthenticateAgentCredential(ctx, token)
	if err != nil || id != "node-1" || generation != 1 {
		t.Fatalf("credential identity: %s %d %v", id, generation, err)
	}
	if err := agentCredentialCommand(ctx, "revoke-agent-credential", []string{"-db", db, "-agent", "node-1"}, &out, &diagnostics); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AuthenticateAgentCredential(ctx, token); err == nil {
		t.Fatal("revoked credential accepted")
	}
}
