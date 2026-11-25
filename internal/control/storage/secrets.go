package storage

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/maxtanforge/moori/internal/core/release"
)

type SecretKey [32]byte
type SecretBindings = release.SecretBindings

const defaultTargetConfig = "ci-default-v1"

var ErrSecretKey = errors.New("secret vault key is unavailable or does not match")
var ErrSecretUnavailable = errors.New("required secret version is missing or revoked")

func (s *Store) CheckSecretKey(ctx context.Context, key SecretKey) error {
	return checkSecretKey(ctx, s.db, key)
}

func (s *Store) HasSecretVault(ctx context.Context) (bool, error) {
	var initialized bool
	err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM secret_vault WHERE singleton=1)").Scan(&initialized)
	return initialized, err
}

func checkSecretKey(ctx context.Context, q queryRower, key SecretKey) error {
	if key == (SecretKey{}) {
		return ErrSecretKey
	}
	var stored []byte
	err := q.QueryRowContext(ctx, "SELECT fingerprint FROM secret_vault WHERE singleton=1").Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	expected := sha256.Sum256(key[:])
	if !bytes.Equal(stored, expected[:]) {
		return ErrSecretKey
	}
	return nil
}

func (s *Store) PutSecret(ctx context.Context, key SecretKey, project, target, name, version string, value []byte) error {
	if project == "" || target == "" || !release.ValidSecretName(name) || !release.ValidSecretVersion(version) ||
		len(value) == 0 || len(value) > release.MaxSecretValueBytes {
		return ErrInvalid
	}
	codec, err := secretCipher(key)
	if err != nil {
		return err
	}
	nonce := make([]byte, codec.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	ciphertext := codec.Seal(nonce, nonce, value, secretAAD(project, target, name, version))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := checkSecretKey(ctx, tx, key); err != nil {
		return err
	}
	fingerprint := sha256.Sum256(key[:])
	if _, err := tx.ExecContext(ctx, "INSERT INTO secret_vault VALUES (1,?) ON CONFLICT DO NOTHING", fingerprint[:]); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO target_secrets(project,target,name,version,ciphertext)
 VALUES (?,?,?,?,?) ON CONFLICT(project,target,name,version) DO NOTHING`, project, target, name, version, ciphertext)
	if err := expectOne(res, err, ErrConflict); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RevokeSecret(ctx context.Context, project, target, name, version string) error {
	res, err := s.db.ExecContext(ctx, "UPDATE target_secrets SET revoked=1 WHERE project=? AND target=? AND name=? AND version=?", project, target, name, version)
	return expectOne(res, err, sql.ErrNoRows)
}

func (s *Store) SetTargetSecrets(ctx context.Context, project, target, version string, input SecretBindings) error {
	if project == "" || target == "" || version == defaultTargetConfig || !release.ValidSecretVersion(version) {
		return ErrInvalid
	}
	bindings, err := input.Canonical()
	if err != nil {
		return errors.Join(ErrInvalid, err)
	}
	raw, err := json.Marshal(bindings)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := requireSecretVersions(ctx, tx, project, target, bindings.Secrets); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO target_secret_configs VALUES (?,?,?,?)
 ON CONFLICT(project,target,version) DO UPDATE SET version=excluded.version WHERE target_secret_configs.bindings=excluded.bindings`,
		project, target, version, string(raw))
	if err := expectOne(res, err, ErrConflict); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO active_target_secrets VALUES (?,?,?)
 ON CONFLICT(project,target) DO UPDATE SET version=excluded.version`, project, target, version); err != nil {
		return err
	}
	return tx.Commit()
}

func requireSecretVersions(ctx context.Context, q queryRower, project, target string, references map[string]string) error {
	for name, version := range references {
		var exists bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM target_secrets
 WHERE project=? AND target=? AND name=? AND version=? AND revoked=0)`, project, target, name, version).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrSecretUnavailable
		}
	}
	return nil
}

func targetSecrets(ctx context.Context, q queryRower, project, target string) (string, SecretBindings, error) {
	var version, raw string
	err := q.QueryRowContext(ctx, `SELECT c.version,c.bindings FROM active_target_secrets a JOIN target_secret_configs c
 ON c.project=a.project AND c.target=a.target AND c.version=a.version WHERE a.project=? AND a.target=?`, project, target).Scan(&version, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return defaultTargetConfig, SecretBindings{}, nil
	}
	if err != nil {
		return "", SecretBindings{}, err
	}
	var b SecretBindings
	err = json.Unmarshal([]byte(raw), &b)
	return version, b, err
}

func secretCipher(key SecretKey) (cipher.AEAD, error) {
	if key == (SecretKey{}) {
		return nil, ErrSecretKey
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func secretAAD(project, target, name, version string) []byte {
	raw, _ := json.Marshal([4]string{project, target, name, version})
	return raw
}
