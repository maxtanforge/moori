package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/control/storage"
)

func TestControlSecretKeyAutomaticallyLoadsAndPreservesVault(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := filepath.Join(dir, "moori.db")
	path := defaultSecretKeyPath(db)
	if path != filepath.Join(dir, "moori.key") {
		t.Fatalf("default path: %s", path)
	}
	store, err := storage.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := controlSecretKey(ctx, store, path, false); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("explicit missing path should not be created: %v", err)
	}
	key, err := controlSecretKey(ctx, store, path, true)
	if err != nil || key == (storage.SecretKey{}) {
		t.Fatalf("automatic initialization failed: %v", err)
	}
	admin, err := store.InitializeAdmin(ctx, "admin", "initial password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := store.CreateHumanRegistry(ctx, admin, key, storage.RegistryInput{Name: "Private", Host: "registry.example", Username: "user", Password: "test-token"})
	if err != nil || !entry.HasCredential {
		t.Fatalf("saving a private registry with the automatic key failed: %v", err)
	}
	if again, err := controlSecretKey(ctx, store, path, true); err != nil || again != key {
		t.Fatalf("existing key was not reused: %v", err)
	}
	backup := filepath.Join(dir, "backup.key")
	if err := os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	if missing, err := controlSecretKey(ctx, store, path, true); err != nil || missing != (storage.SecretKey{}) {
		t.Fatalf("missing vault key should disable credential access: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("automatic startup replaced the missing original key: %v", err)
	}
	wrong := filepath.Join(dir, "wrong.key")
	if err := createSecretKey(wrong); err != nil {
		t.Fatal(err)
	}
	if _, err := controlSecretKey(ctx, store, wrong, false); !errors.Is(err, storage.ErrSecretKey) {
		t.Fatalf("wrong key was accepted: %v", err)
	}
	if restored, err := controlSecretKey(ctx, store, backup, false); err != nil || restored != key {
		t.Fatalf("explicit original key was not accepted: %v", err)
	}
}
