package secrets

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSecretGCProtectsMountedAndYoungBundles(t *testing.T) {
	root := t.TempDir()
	// t.TempDir() returns a numbered subdirectory created with 0777&^umask
	// (0755 under umask 0022); CollectUnused requires a private root, so
	// tighten it explicitly instead of relying on the ambient umask.
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	for _, name := range []string{"bundle-unused", "bundle-mounted", "bundle-young", "bundle-mounted-prefix"} {
		path := filepath.Join(root, name)
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "secret"), []byte("value"), 0444); err != nil {
			t.Fatal(err)
		}
		if name != "bundle-young" {
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	mounts := []string{filepath.Join(root, "bundle-mounted", "secret")}
	before := time.Now().Add(-24 * time.Hour)
	candidates, err := CollectUnused(root, mounts, before, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 {
		t.Fatal(candidates)
	}
	if _, err := os.Stat(filepath.Join(root, "bundle-unused", "secret")); err != nil {
		t.Fatal("dry run removed file")
	}
	if _, err := CollectUnused(root, []string{root}, before, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "bundle-unused")); err != nil {
		t.Fatal("parent mount not protected")
	}
	if _, err := CollectUnused(root, mounts, before, true); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bundle-mounted", "bundle-young"} {
		if _, err := os.Stat(filepath.Join(root, name, "secret")); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"bundle-unused", "bundle-mounted-prefix"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatal("unused bundle retained", err)
		}
	}
	for _, name := range []string{"bundle-mounted", "bundle-young"} {
		_ = os.Chmod(filepath.Join(root, name, "secret"), 0600)
	}
}

func TestSecretGCRejectsInvalidInventoryBeforeDeletion(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "bundle-a")
	if err := os.MkdirAll(filepath.Join(path, "unexpected"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := CollectUnused(root, nil, time.Now().Add(time.Hour), true); err == nil {
		t.Fatal("nested directory accepted")
	}
	if _, err := CollectUnused(root, []string{"relative"}, time.Now(), true); err == nil {
		t.Fatal("relative mount accepted")
	}
}
