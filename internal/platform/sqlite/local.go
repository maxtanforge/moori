// Package sqlite opens dedicated local databases for the control and Agent stores.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	driver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// DefaultPath locates a default database in the configured data directory.
func DefaultPath(name string) string {
	return filepath.Join(os.Getenv("MOORI_DATA_DIR"), name)
}

func Open(ctx context.Context, path string, applicationID int, migrations ...string) (*sql.DB, error) {
	uri, err := databaseURI(path)
	if err != nil {
		return nil, err
	}
	uri.RawQuery = "_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(FULL)"
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	// WAL permits parallel readers, so a small pool lets independent
	// autocommit reads (console polls, dispatch lookups, list queries)
	// overlap. Transactions are unaffected: the DSN pins every BeginTx to
	// IMMEDIATE, so writes serialize exactly as before and busy_timeout
	// absorbs contention between them.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	if err := migrate(ctx, db, applicationID, migrations); err != nil {
		db.Close()
		return nil, databaseStartupError("initialize", path, err)
	}
	var journal string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode(WAL)").Scan(&journal); err != nil {
		db.Close()
		return nil, databaseStartupError("enable WAL for", path, err)
	}
	if journal != "wal" {
		db.Close()
		return nil, fmt.Errorf("SQLite did not enable WAL: %q", journal)
	}
	return db, nil
}

func databaseStartupError(operation, path string, cause error) error {
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	failure := fmt.Errorf("%s SQLite database %q: %w", operation, path, cause)
	var sqliteErr *driver.Error
	if !errors.As(cause, &sqliteErr) {
		return failure
	}
	// The primary result code occupies the low byte of an extended result code.
	switch sqliteErr.Code() & 0xff {
	case sqlite3.SQLITE_CANTOPEN:
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			return fmt.Errorf("%w (database path is a directory)", failure)
		}
		parent := filepath.Dir(path)
		if info, err := os.Stat(parent); errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w (database parent directory does not exist: %q)", failure, parent)
		} else if err == nil && !info.IsDir() {
			return fmt.Errorf("%w (database parent path is not a directory: %q)", failure, parent)
		}
		fallthrough
	case sqlite3.SQLITE_READONLY:
		return fmt.Errorf("%w (check that the database and its parent directory are writable by the service user)", failure)
	default:
		return failure
	}
}

func databaseURI(path string) (url.URL, error) {
	if path == "" {
		return url.URL{}, fmt.Errorf("database path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return url.URL{}, err
	}
	name := filepath.ToSlash(absolute)
	if !strings.HasPrefix(name, "/") {
		name = "/" + name
	}
	return url.URL{Scheme: "file", Path: name}, nil
}

func migrate(ctx context.Context, db *sql.DB, applicationID int, migrations []string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version, owner int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, "PRAGMA application_id").Scan(&owner); err != nil {
		return err
	}
	switch {
	case version == 0 && owner == 0:
		var tables int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
			return err
		}
		if tables != 0 {
			return fmt.Errorf("refusing to initialize a nonempty database")
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA application_id = %d", applicationID)); err != nil {
			return err
		}
	case version < 1 || version > len(migrations) || owner != applicationID:
		return fmt.Errorf("unsupported database identity %d or schema version %d", owner, version)
	}
	for i := version; i < len(migrations); i++ {
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			return err
		}
	}
	return tx.Commit()
}
