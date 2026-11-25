package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/maxtanforge/moori/internal/control/storage"
)

// ensureDestinationDir rejects destinations whose directory is missing so the
// operator sees the actual problem instead of a low-level SQLite open error.
func ensureDestinationDir(dest string) error {
	dir := filepath.Dir(dest)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("output directory does not exist: %s", dir)
	}
	return nil
}

func backupCommand(ctx context.Context, command string, args []string) error {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	switch command {
	case "backup-db":
		live := flags.String("db", defaultDB(), "existing control database")
		dest := flags.String("out", "", "new destination on a local filesystem with hard-link support")
		if err := parseFlags(flags, args); err != nil {
			return err
		}
		if *dest == "" {
			return errors.New("backup-db requires -out")
		}
		if err := ensureDestinationDir(*dest); err != nil {
			return err
		}
		if err := storage.BackupDatabase(ctx, *live, *dest); err != nil {
			return err
		}
		_, err := fmt.Fprintln(os.Stdout, "Created and verified control database backup")
		return err
	case "verify-backup":
		path := flags.String("file", "", "standalone control database backup")
		if err := parseFlags(flags, args); err != nil {
			return err
		}
		if *path == "" {
			return errors.New("verify-backup requires -file")
		}
		if err := storage.VerifyBackup(ctx, *path); err != nil {
			return err
		}
		_, err := fmt.Fprintln(os.Stdout, "Control database backup verified")
		return err
	case "prepare-restore":
		source := flags.String("file", "", "verified control backup")
		destination := flags.String("out", "", "new control database path")
		if err := parseFlags(flags, args); err != nil {
			return err
		}
		if *source == "" || *destination == "" {
			return errors.New("prepare-restore requires -file and -out")
		}
		if err := ensureDestinationDir(*destination); err != nil {
			return err
		}
		if err := storage.PrepareRestore(ctx, *source, *destination); err != nil {
			return err
		}
		_, err := fmt.Fprintln(os.Stdout, "Prepared control database in recovery mode")
		return err
	case "restore-release-target":
		path := flags.String("db", "", "existing restored control database")
		epoch := flags.String("epoch", "", "current recovery epoch")
		project := flags.String("project", "", "project ID")
		target := flags.String("target", "", "target ID")
		operator := flags.String("operator", "", "operator identity for the audit record")
		note := flags.String("note", "", "Docker inspection and verification that prior operations stopped")
		if err := parseFlags(flags, args); err != nil {
			return err
		}
		if *path == "" || *epoch == "" || *project == "" || *target == "" || *operator == "" || *note == "" {
			return errors.New("restore-release-target requires -db, -epoch, -project, -target, -operator and -note")
		}
		if _, err := os.Stat(*path); err != nil {
			return err
		}
		store, err := storage.Open(ctx, *path)
		if err != nil {
			return err
		}
		defer store.Close()
		if err := store.ReleaseRestoredTarget(ctx, *epoch, *project, *target, *operator, *note); err != nil {
			return err
		}
		_, err = fmt.Fprintln(os.Stdout, "Target released for deployment")
		return err
	case "restore-status", "restore-inventory":
		path := flags.String("db", "", "existing control database")
		var agent, after string
		if command == "restore-inventory" {
			flags.StringVar(&agent, "agent", "", "Agent ID")
			flags.StringVar(&after, "after", "", "execution ID from the previous page")
		}
		if err := parseFlags(flags, args); err != nil {
			return err
		}
		if *path == "" {
			return errors.New("restore inspection requires -db")
		}
		if command == "restore-inventory" && agent == "" {
			return errors.New("restore-inventory requires -agent")
		}
		if _, err := os.Stat(*path); err != nil {
			return err
		}
		store, err := storage.Open(ctx, *path)
		if err != nil {
			return err
		}
		defer store.Close()
		if command == "restore-inventory" {
			view, err := store.ReadRecoveryInventory(ctx, agent, after)
			if err != nil {
				return err
			}
			return json.NewEncoder(os.Stdout).Encode(view)
		}
		view, err := store.RecoveryStatus(ctx)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(view)
	default:
		return errors.New(controlUsage)
	}
}
