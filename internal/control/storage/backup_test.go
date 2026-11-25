package storage

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/maxtanforge/moori/internal/node/ledger"
)

func TestBackupCopiesCommittedStateWithoutOverwriting(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	live := filepath.Join(dir, "control.db")
	dest := filepath.Join(dir, "backup with spaces.db")
	origin := openTestStore(t, live)
	if err := origin.RegisterTarget(ctx, "project", "before"); err != nil {
		t.Fatal(err)
	}
	if err := BackupDatabase(ctx, live, dest); err != nil {
		t.Fatal(err)
	}
	if err := VerifyBackup(ctx, dest); err != nil {
		t.Fatal(err)
	}
	if err := origin.RegisterTarget(ctx, "project", "after"); err != nil {
		t.Fatal(err)
	}
	if err := BackupDatabase(ctx, live, dest); err == nil {
		t.Fatal("overwrote existing backup")
	}
	if snapshot, err := Open(ctx, dest); err == nil {
		snapshot.Close()
		t.Fatal("backup opened as a live control database")
	}
	if err := VerifyBackup(ctx, dest); err != nil {
		t.Fatalf("rejected startup changed backup: %v", err)
	}
	snapshot, err := sql.Open("sqlite", dest)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	var first, second bool
	if err := snapshot.QueryRow("SELECT EXISTS(SELECT 1 FROM targets WHERE id='before'), EXISTS(SELECT 1 FROM targets WHERE id='after')").Scan(&first, &second); err != nil || !first || second {
		t.Fatalf("snapshot states: %v %v %v", first, second, err)
	}
}

func TestBackupVerificationRejectsBadInput(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	bad := filepath.Join(dir, "corrupt.db")
	if err := os.WriteFile(bad, []byte("not a SQLite database"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyBackup(ctx, bad); err == nil {
		t.Fatal("corrupt backup accepted")
	}
	missing := filepath.Join(dir, "missing.db")
	if err := BackupDatabase(ctx, missing, filepath.Join(dir, "destination.db")); err == nil {
		t.Fatal("missing source accepted")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("source was accidentally created: %v", err)
	}
	other := filepath.Join(dir, "agent.db")
	book, err := ledger.Open(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	if err := book.Close(); err != nil {
		t.Fatal(err)
	}
	if err := VerifyBackup(ctx, other); err == nil {
		t.Fatal("Agent database accepted as control backup")
	}
}
