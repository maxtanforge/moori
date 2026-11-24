package storage

import (
	"context"

	"github.com/maxtanforge/moori/internal/platform/sqlite"
)

const controlBackupID = 0x4d4f4f62

func BackupDatabase(ctx context.Context, source, destination string) error {
	return sqlite.Backup(ctx, source, destination, controlApplicationID, controlBackupID, controlSchemaVersion)
}

func VerifyBackup(ctx context.Context, path string) error {
	return sqlite.VerifyBackup(ctx, path, controlBackupID, controlSchemaVersion)
}
