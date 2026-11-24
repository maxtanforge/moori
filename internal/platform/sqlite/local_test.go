package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestOpenReportsDatabaseLayoutErrors(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "directory.db")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path, reason string
	}{
		{filepath.Join(root, "missing", "moori.db"), "database parent directory does not exist"},
		{directory, "database path is a directory"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			db, err := Open(context.Background(), tc.path, 1, "CREATE TABLE example (id INTEGER)")
			if err == nil {
				db.Close()
				t.Fatal("opened invalid database path")
			}
			if !strings.Contains(err.Error(), strconv.Quote(tc.path)) || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("missing path or reason: %v", err)
			}
		})
	}
}
