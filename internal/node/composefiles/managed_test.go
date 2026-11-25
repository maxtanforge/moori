package composefiles

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestManagedGenerationPreservesSourcesAcrossRecreation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "managed")
	content := "# keep this comment\nservices:\n  app:\n    image: nginx\n"
	s, snap, err := CreateManaged(root, "app", content, func(s *Sources, snap Snapshot) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	opened, err := OpenManaged(root, "app", s.root)
	if err != nil {
		t.Fatal(err)
	}
	read, err := opened.Read()
	if err != nil || read.Revision != snap.Revision || read.Files[0] != (File{"compose.yaml", content}) {
		t.Fatalf("read %#v %v", read, err)
	}
	second, _, err := CreateManaged(root, "app", content+"# new generation\n", func(*Sources, Snapshot) error { return nil })
	if err != nil || second.root == s.root {
		t.Fatalf("recreate %v", err)
	}
	if _, err := OpenManaged(root, "unrelated", s.root); !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("unrelated: %v", err)
	}
	if _, err := OpenManaged(root, "app", filepath.Dir(root)); !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("external: %v", err)
	}
}

func TestManagedValidationFailureDiscardsSource(t *testing.T) {
	root := filepath.Join(t.TempDir(), "managed")
	expected := errors.New("docker config failed")
	_, _, err := CreateManaged(root, "app", "services: {}", func(*Sources, Snapshot) error { return expected })
	if !errors.Is(err, expected) {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("residue %v %v", entries, err)
	}
}

func TestManagedModelRejectsRelativeBindPaths(t *testing.T) {
	for _, tc := range []struct {
		typ, src string
		valid    bool
	}{
		{"bind", "./data", false}, {"bind", "~/data", false}, {"bind", "data", false}, {"bind", "/srv/data", true}, {"volume", "data", true},
	} {
		data := fmt.Sprintf(`{"services":{"app":{"volumes":[{"type":%q,"source":%q}]}}}`, tc.typ, tc.src)
		names, err := managedServices([]byte(data))
		if (err == nil) != tc.valid || tc.valid && (len(names) != 1 || names[0] != "app") {
			t.Fatalf("%s: %v %v", tc.src, names, err)
		}
	}
}
