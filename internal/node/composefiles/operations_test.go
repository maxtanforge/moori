package composefiles

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

const testManifest = `{"app":["project/compose.yaml","project/override.yaml"]}`
const baseDocument = "# retain this comment\nservices:\n  web:\n    image: nginx:alpine\n"
const overrideDocument = "services:\n  web:\n    restart: always\n"

func sourceFixture(t *testing.T) (string, *Sources) {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "project"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"compose.yaml": baseDocument, "override.yaml": overrideDocument} {
		if err := os.WriteFile(filepath.Join(root, "project", name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	sources, err := Open(root, testManifest, "app")
	if err != nil {
		t.Fatal(err)
	}
	return root, sources
}

func TestSourceRevisionAndSingleFileSave(t *testing.T) {
	root, sources := sourceFixture(t)
	before, err := sources.Read()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile("^[a-f0-9]{64}$").MatchString(before.Revision) || len(before.Files) != 2 ||
		before.Files[0] != (File{"project/compose.yaml", baseDocument}) || before.Files[1] != (File{"project/override.yaml", overrideDocument}) {
		t.Fatalf("unexpected initial snapshot: %+v", before)
	}
	rejected := errors.New("invalid by Compose validation")
	if _, err := sources.SaveValidated(before.Revision, before.Files[1].Path, "services: {}\n", func(old, edited Snapshot) error {
		if old.Revision != before.Revision || edited.Files[0] != old.Files[0] || edited.Files[1].Content != "services: {}\n" {
			t.Fatal("validator did not receive the ordered snapshots")
		}
		return rejected
	}); !errors.Is(err, rejected) {
		t.Fatalf("validation error = %v", err)
	}
	snapshot, err := sources.Read()
	if err != nil || snapshot.Revision != before.Revision {
		t.Fatalf("validation failure mutated sources: %v", err)
	}
	next, err := sources.Save(before.Revision, before.Files[1].Path, "services: {}\n")
	if err != nil || next.Revision == before.Revision || next.Files[0] != before.Files[0] {
		t.Fatalf("unexpected save result: %v", err)
	}
	if _, err := sources.Save(before.Revision, before.Files[0].Path, baseDocument); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale base accepted: %v", err)
	}
	if _, err := sources.Save(next.Revision, "project/other.yaml", baseDocument); !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("unregistered path accepted: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "project", "override.yaml"))
	if err != nil || string(got) != "services: {}\n" {
		t.Fatalf("save did not replace the selected file: %v", err)
	}
}

func TestDiscoveredSourcesPreserveOrderAndStayInsideRoot(t *testing.T) {
	root, _ := sourceFixture(t)
	base := filepath.Join(root, "project", "compose.yaml")
	override := filepath.Join(root, "project", "override.yaml")
	sources, err := OpenDiscovered(root, "app", []string{base, override})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := sources.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Files) != 2 || snapshot.Files[0].Content != baseDocument || snapshot.Files[1].Content != overrideDocument {
		t.Fatalf("wrong source order: %+v", snapshot.Files)
	}
	if _, err := OpenDiscovered(root, "app", []string{filepath.Join(filepath.Dir(root), "compose.yaml")}); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("outside path was not rejected: %v", err)
	}
}

func TestSourceRejectsLinkAndUnregisteredProject(t *testing.T) {
	root, sources := sourceFixture(t)
	if _, err := Open(root, testManifest, "missing"); !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("unregistered project accepted: %v", err)
	}
	if _, err := Open(root, `{"app":["../child.yaml"]}`, "app"); err == nil {
		t.Fatal("escaped source path accepted")
	}
	target := filepath.Join(root, "project", "override.yaml")
	link := filepath.Join(root, "project", "compose.yaml")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	if _, err := sources.Read(); err == nil {
		t.Fatal("symbolic link accepted as a Compose source")
	}
}

func TestSourceSaveDetectsAnotherFileChangedDuringValidation(t *testing.T) {
	root, sources := sourceFixture(t)
	before, err := sources.Read()
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "project", "compose.yaml")
	other := filepath.Join(root, "project", "override.yaml")
	document := baseDocument + "# new edit\n"
	_, err = sources.SaveValidated(before.Revision, before.Files[0].Path, document, func(_, _ Snapshot) error {
		return os.WriteFile(other, []byte(overrideDocument+"# external edit\n"), 0600)
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("concurrent change during validation: %v", err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != baseDocument {
		t.Fatalf("stale group nevertheless saved the selected file: %v", err)
	}

	latest, err := sources.Read()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0640); err != nil {
		t.Fatal(err)
	}
	original, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sources.Save(latest.Revision, before.Files[0].Path, document); err != nil {
		t.Fatal(err)
	}
	updated, err := os.Stat(target)
	if err != nil || updated.Mode().Perm() != original.Mode().Perm() {
		t.Fatalf("replaced file changed original permissions: %v", err)
	}
}
