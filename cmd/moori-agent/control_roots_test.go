package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestControlRootsUsesSystemTrustByDefault(t *testing.T) {
	roots, err := controlRoots("")
	if err != nil || roots != nil {
		t.Fatalf("default trust override: %v, %v", roots, err)
	}
	path := filepath.Join(t.TempDir(), "invalid.pem")
	if err := os.WriteFile(path, []byte("not a CA"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := controlRoots(path); err == nil {
		t.Fatal("invalid CA accepted")
	}
}
