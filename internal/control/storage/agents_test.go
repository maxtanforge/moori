package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestCertificateRotationInvalidatesPriorGeneration(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	first, second := [32]byte{1}, [32]byte{2}
	if err := s.BindAgent(ctx, "agent", 1, first); err != nil {
		t.Fatal(err)
	}
	if err := s.BindAgent(ctx, "agent", 2, second); err != nil {
		t.Fatal(err)
	}
	if err := s.BindAgent(ctx, "agent", 1, first); !errors.Is(err, ErrConflict) {
		t.Fatalf("older identity replaced current identity: %v", err)
	}
	if _, _, err := s.ResolveAgent(ctx, first); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("old certificate accepted: %v", err)
	}
	agent, generation, err := s.ResolveAgent(ctx, second)
	if err != nil || agent != "agent" || generation != 2 {
		t.Fatalf("new identity: %s %d %v", agent, generation, err)
	}
	if err := s.ValidateAgent(ctx, "agent", 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("old connection still accepted: %v", err)
	}
}
