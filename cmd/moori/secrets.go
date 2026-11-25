package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/core/release"
)

func initSecretKey(args []string) error {
	f := flag.NewFlagSet("init-secret-key", flag.ContinueOnError)
	path := f.String("out", "", "new file for the 32-byte control secret key")
	if err := parseFlags(f, args); err != nil {
		return err
	}
	if *path == "" {
		return errors.New("init-secret-key requires -out")
	}
	return createSecretKey(*path)
}

func defaultSecretKeyPath(db string) string {
	if filepath.Base(db) == "moori.db" {
		return filepath.Join(filepath.Dir(db), "moori.key")
	}
	return db + ".key"
}

func controlSecretKey(ctx context.Context, store *storage.Store, path string, automatic bool) (storage.SecretKey, error) {
	key, err := readSecretKey(path)
	if automatic && errors.Is(err, os.ErrNotExist) {
		initialized, err := store.HasSecretVault(ctx)
		if err != nil || initialized {
			return storage.SecretKey{}, err
		}
		if err := createSecretKey(path); err != nil && !errors.Is(err, os.ErrExist) {
			return storage.SecretKey{}, fmt.Errorf("create control secret key %q: %w", path, err)
		}
		// A concurrent start may have published the file first. Both load it.
		key, err = readSecretKey(path)
		if err != nil {
			return storage.SecretKey{}, fmt.Errorf("load control secret key %q: %w", path, err)
		}
	} else if err != nil {
		return storage.SecretKey{}, fmt.Errorf("load control secret key %q: %w", path, err)
	}
	if err := store.CheckSecretKey(ctx, key); err != nil {
		return storage.SecretKey{}, fmt.Errorf("verify control secret key %q against database: %w", path, err)
	}
	return key, nil
}

func createSecretKey(path string) error {
	var key storage.SecretKey
	if _, err := rand.Read(key[:]); err != nil {
		return err
	}
	defer clear(key[:])
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, ".moori-key-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, err = file.Write(key[:])
	if err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	// Publish the fully written key without replacing an existing file.
	if err := os.Link(file.Name(), path); err != nil {
		return err
	}
	if runtime.GOOS == "linux" {
		parent, err := os.Open(dir)
		if err != nil {
			return err
		}
		return errors.Join(parent.Sync(), parent.Close())
	}
	return nil
}

func readSecretKey(path string) (storage.SecretKey, error) {
	var key storage.SecretKey
	file, err := os.Open(path)
	if err != nil {
		return key, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return key, err
	}
	if !info.Mode().IsRegular() || info.Size() != int64(len(key)) || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return key, errors.New("expected a private 32-byte secret key file")
	}
	if _, err := io.ReadFull(file, key[:]); err != nil {
		return storage.SecretKey{}, err
	}
	return key, nil
}

func parseSecretCommand(f *flag.FlagSet, command string, args []string) (localAction, error) {
	project := f.String("project", "", "project ID")
	target := f.String("target", "", "target ID")
	version := f.String("version", "", "immutable version")
	if command == "set-target-secrets" {
		path := f.String("file", "", "JSON mapping secret names to versions and services")
		if err := parseFlags(f, args); err != nil {
			return nil, err
		}
		if *path == "" {
			return nil, errors.New("set-target-secrets requires -file")
		}
		return func(ctx context.Context, s *storage.Store) error {
			file, err := os.Open(*path)
			if err != nil {
				return err
			}
			defer file.Close()
			const limit = 64 << 10
			reader := &io.LimitedReader{R: file, N: limit + 1}
			dec := json.NewDecoder(reader)
			dec.DisallowUnknownFields()
			var bindings storage.SecretBindings
			if err := dec.Decode(&bindings); err != nil {
				return errors.New("invalid secret bindings JSON")
			}
			if dec.Decode(new(json.RawMessage)) != io.EOF || reader.N == 0 {
				return errors.New("expected one bounded secret bindings object")
			}
			return s.SetTargetSecrets(ctx, *project, *target, *version, bindings)
		}, nil
	}
	name := f.String("name", "", "secret name")
	if command == "revoke-secret" {
		if err := parseFlags(f, args); err != nil {
			return nil, err
		}
		return func(ctx context.Context, s *storage.Store) error {
			return s.RevokeSecret(ctx, *project, *target, *name, *version)
		}, nil
	}
	keyPath := f.String("secret-key", "", "private 32-byte control secret key file")
	if err := parseFlags(f, args); err != nil {
		return nil, err
	}
	if *keyPath == "" {
		return nil, errors.New("put-secret requires -secret-key and exact value bytes on stdin")
	}
	return func(ctx context.Context, s *storage.Store) error {
		key, err := readSecretKey(*keyPath)
		if err != nil {
			return err
		}
		defer clear(key[:])
		value, err := io.ReadAll(io.LimitReader(os.Stdin, release.MaxSecretValueBytes+1))
		if err != nil {
			return err
		}
		defer clear(value)
		return s.PutSecret(ctx, key, *project, *target, *name, *version, value)
	}, nil
}
