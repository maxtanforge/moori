package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// A missing output directory used to surface as a low-level SQLite open
// error; both backup-db and prepare-restore must name the real problem.
func TestBackupCommandsReportMissingOutputDirectory(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing", "backup.db")
	for _, tc := range []struct {
		command string
		args    []string
	}{
		{"backup-db", []string{"-db", filepath.Join(dir, "moori.db"), "-out", missing}},
		{"prepare-restore", []string{"-file", filepath.Join(dir, "control.db"), "-out", missing}},
	} {
		t.Run(tc.command, func(t *testing.T) {
			err := backupCommand(context.Background(), tc.command, tc.args)
			if err == nil || !strings.Contains(err.Error(), "output directory does not exist") {
				t.Fatalf("%s error = %v, want missing output directory hint", tc.command, err)
			}
		})
	}
}
