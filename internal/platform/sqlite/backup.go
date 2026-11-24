package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Backup copies a live WAL database to a standalone SQLite snapshot.
func Backup(ctx context.Context, source, destination string, applicationID, backupID, maxVersion int) error {
	return publishSnapshot(ctx, source, destination, false, applicationID, maxVersion, func(stage string) error {
		return relabel(ctx, stage, applicationID, backupID, maxVersion)
	}, backupID)
}

// PrepareRestore copies and validates the snapshot, then applies the
// application's recovery barrier before the new live-format file is visible.
func PrepareRestore(ctx context.Context, source, destination string, backupID, applicationID, maxVersion int, prepare func(string) error) error {
	if err := VerifyBackup(ctx, source, backupID, maxVersion); err != nil {
		return err
	}
	return publishSnapshot(ctx, source, destination, true, backupID, maxVersion, func(stage string) error {
		if err := relabel(ctx, stage, backupID, applicationID, maxVersion); err != nil {
			return err
		}
		return prepare(stage)
	}, applicationID)
}

func publishSnapshot(ctx context.Context, source, destination string, standalone bool, sourceID, maxVersion int, prepare func(string) error, destinationID int) error {
	if destination == "" {
		return errors.New("backup destination is required")
	}
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("backup destination already exists: %w", os.ErrExist)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	db, err := openExisting(source, standalone)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := checkIdentity(ctx, db, sourceID, maxVersion); err != nil {
		return err
	}

	temporary, err := os.CreateTemp(filepath.Dir(destination), ".moori-backup-*.sqlite")
	if err != nil {
		return err
	}
	stage := temporary.Name()
	defer func() {
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			_ = os.Remove(stage + suffix)
		}
	}()
	if err := temporary.Close(); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", stage); err != nil {
		return fmt.Errorf("create SQLite snapshot: %w", err)
	}
	if err := prepare(stage); err != nil {
		return err
	}
	if err := VerifyBackup(ctx, stage, destinationID, maxVersion); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Hard-link publication is atomic and fails rather than overwriting an
	// existing path, even if another process created it after the initial check.
	if err := os.Link(stage, destination); err != nil {
		return fmt.Errorf("publish backup on destination filesystem: %w", err)
	}
	return nil
}

// Relabel only the staging file, never the live database or original backup.
func relabel(ctx context.Context, path string, applicationID, destinationID, maxVersion int) error {
	uri, err := databaseURI(path)
	if err != nil {
		return err
	}
	uri.RawQuery = "mode=rw&_pragma=journal_mode(DELETE)&_pragma=trusted_schema(OFF)&_pragma=synchronous(FULL)"
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := checkIdentity(ctx, db, applicationID, maxVersion); err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, fmt.Sprintf("PRAGMA application_id = %d", destinationID))
	return err
}

// VerifyBackup opens only a standalone snapshot. It never migrates the
// source or creates supplementary SQLite files during verification.
func VerifyBackup(ctx context.Context, path string, applicationID, maxVersion int) error {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(path + suffix); err == nil {
			return errors.New("backup must be standalone without SQLite sidecar files")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	db, err := openExisting(path, true)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := checkIdentity(ctx, db, applicationID, maxVersion); err != nil {
		return err
	}
	var integrity string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check(1)").Scan(&integrity); err != nil {
		return err
	}
	if integrity != "ok" {
		return errors.New("backup did not pass the SQLite integrity check")
	}
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("backup contains invalid foreign keys")
	}
	return rows.Err()
}

func openExisting(path string, standalone bool) (*sql.DB, error) {
	uri, err := databaseURI(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("expected a regular SQLite file")
	}
	uri.RawQuery = "mode=ro&_pragma=busy_timeout(5000)&_pragma=trusted_schema(OFF)&_pragma=synchronous(FULL)"
	if standalone {
		uri.RawQuery += "&immutable=1"
	}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func checkIdentity(ctx context.Context, db *sql.DB, applicationID, maxVersion int) error {
	var owner, version int
	if err := db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&owner); err != nil {
		return err
	}
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if owner != applicationID || version < 1 || version > maxVersion {
		return fmt.Errorf("unsupported SQLite application ID %d or schema version %d", owner, version)
	}
	return nil
}
