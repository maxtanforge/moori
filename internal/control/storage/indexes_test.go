package storage

import (
	"path/filepath"
	"testing"

	"github.com/maxtanforge/moori/internal/platform/sqlite"
)

// The secondary indexes must reach databases initialized before the migration,
// not only fresh installs.
func TestSecondaryIndexMigrationCoversExistingDatabases(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "upgrade.db")
	old, err := sqlite.Open(ctx, path, controlApplicationID, schema)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.ExecContext(ctx, "INSERT INTO targets(id,project) VALUES ('app-production','app')"); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	s := openTestStore(t, path)
	defer s.Close()
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema
 WHERE type='index' AND name IN ('deployments_target_position','node_operations_generation_time')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("expected both secondary indexes after migration, found %d", count)
	}
	if _, err := s.PageCIPipelineRuns(ctx, "unused-token", "app-production", "", 20); err == nil {
		t.Fatal("expected authentication failure, not a query error")
	}
}
