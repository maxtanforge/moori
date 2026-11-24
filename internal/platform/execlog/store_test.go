package execlog

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPeekIsolatesCorruptHeadAndContinues(t *testing.T) {
	s, err := Open(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	path := s.path("broken")
	if err := os.WriteFile(path, []byte("bad json"), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if err := s.resyncLocked(); err != nil {
		t.Fatal(err)
	}
	good := Entry{Execution: "healthy", Text: "new output"}
	if err := s.Put(good); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Peek(); err != nil || got != good {
		t.Fatalf("got %+v, %v", got, err)
	}
	raw, err := os.ReadFile(filepath.Join(s.dir, ".corrupt-log"))
	if err != nil || string(raw) != "bad json" {
		t.Fatalf("quarantine: %q, %v", raw, err)
	}
}

func TestCaptureKeepsBoundedTail(t *testing.T) {
	var b Tail
	b.Write([]byte(strings.Repeat("x", TailBytes+1)))
	b.Write([]byte("failure details"))
	entry := b.Entry("attempt")
	if !entry.Truncated || len(entry.Text) != TailBytes || !strings.HasSuffix(entry.Text, "failure details") {
		t.Fatalf("invalid tail length=%d, truncated=%v", len(entry.Text), entry.Truncated)
	}
}

func TestStoreLimitsDiskAndKeepsImmutableEntries(t *testing.T) {
	path := t.TempDir()
	s, err := Open(path, 1024)
	if err != nil {
		t.Fatal(err)
	}
	a := Entry{Execution: "../first", Text: strings.Repeat("a", 600)}
	b := Entry{Execution: "second", Text: strings.Repeat("b", 600)}
	if err := s.Put(a); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(a); err != nil {
		t.Fatal(err)
	}
	changed := a
	changed.Text = "modified"
	if err := s.Put(changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("immutable log changed: %v", err)
	}
	if err := s.Put(b); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(a.Execution); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old entry was not evicted: %v", err)
	}
	s, err = Open(path, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.Peek(); err != nil || got != b {
		t.Fatalf("persisted entry: %+v %v", got, err)
	}
	if err := s.Remove(b.Execution); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Peek(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("acknowledged entry remains: %v", err)
	}
}
