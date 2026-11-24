package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"time"
)

var ErrInitialized = errors.New("installation already initialized")
var ErrUsernameTaken = errors.New("username already in use")

const setupAvailable = `NOT EXISTS(SELECT 1 FROM installation) AND NOT EXISTS(SELECT 1 FROM human_users) AND NOT EXISTS(SELECT 1 FROM recovery_state)`

func (s *Store) SetupRequired(ctx context.Context) (bool, error) {
	var required bool
	err := s.db.QueryRowContext(ctx, `SELECT `+setupAvailable).Scan(&required)
	return required, err
}

// InitializeAdmin commits the first user, platform grant, audit and session
// together. SQLite's immediate transaction serializes competing setup requests.
func (s *Store) InitializeAdmin(ctx context.Context, username, password string, ttl time.Duration) (string, error) {
	username = normalizeUsername(username)
	if !validUsername(username) || ttl < time.Second || ttl > 24*time.Hour {
		return "", ErrInvalid
	}
	required, err := s.SetupRequired(ctx)
	if err != nil {
		return "", err
	}
	if !required {
		return "", ErrInitialized
	}
	salt, digest, err := newPassword(password)
	if err != nil {
		return "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, `SELECT `+setupAvailable).Scan(&required); err != nil {
		return "", err
	}
	if !required {
		return "", ErrInitialized
	}
	id := "human:" + rand.Text()
	if _, err := tx.ExecContext(ctx, `INSERT INTO human_users(id,username,salt,password_hash) VALUES(?,?,?,?)`, id, username, salt, digest); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO platform_admins(user_id) VALUES(?)`, id); err != nil {
		return "", err
	}
	if err := addProjectAudit(ctx, tx, "", "platform.initialized", id, id, ""); err != nil {
		return "", err
	}
	// Use the same token format as login.
	token, hash, err := newHumanToken()
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO human_sessions(token_hash,user_id,expires_ms) VALUES(?,?,?)`, hash[:], id, time.Now().Add(ttl).UnixMilli()); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return token, nil
}

func (s *Store) ChangeHumanAccount(ctx context.Context, token, username, current, replacement string) error {
	username = normalizeUsername(username)
	if !validUsername(username) {
		return ErrInvalid
	}
	hash, err := humanTokenHash(token)
	if err != nil {
		return err
	}
	user, err := findHumanSession(ctx, s.db, hash)
	if err != nil {
		return err
	}
	row := s.db.QueryRowContext(ctx, "SELECT "+humanPasswordFields+" FROM human_users WHERE id=?", user)
	old, err := s.verifyHuman(ctx, row, current)
	if err != nil {
		return err
	}
	salt, digest := old.salt, old.digest
	var replacementErr error
	if replacement != "" {
		salt, digest, replacementErr = newPassword(replacement)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := findHumanSession(ctx, tx, hash); err != nil {
		return err
	}
	// Recheck after the password work. A concurrent failure may have locked
	// the account before the transaction, so defer business errors until then.
	now := time.Now().UnixMilli()
	var eligible bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM human_users WHERE `+humanPasswordEligible+`)`,
		old.id, old.username, old.salt, old.digest, now).Scan(&eligible); err != nil {
		return err
	}
	if !eligible {
		return ErrForbidden
	}
	if replacementErr != nil {
		return replacementErr
	}
	var taken bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM human_users WHERE username=? AND id<>?)`, username, user).Scan(&taken); err != nil {
		return err
	}
	if taken {
		return ErrUsernameTaken
	}
	res, err := tx.ExecContext(ctx, `UPDATE human_users SET username=?,salt=?,password_hash=?,password_failures=0,password_locked_until_ms=0
 WHERE `+humanPasswordEligible+``,
		username, salt, digest, user, old.username, old.salt, old.digest, now)
	if err := expectOne(res, err, ErrForbidden); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM human_sessions WHERE user_id=?`, user); err != nil {
		return err
	}
	if err := addProjectAudit(ctx, tx, "", "account.changed", user, user, ""); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) HumanUsername(ctx context.Context, token string) (string, error) {
	hash, err := humanTokenHash(token)
	if err != nil {
		return "", err
	}
	var name string
	err = s.db.QueryRowContext(ctx, `SELECT u.username FROM human_users u JOIN human_sessions s ON s.user_id=u.id WHERE s.token_hash=? AND s.expires_ms>?`, hash[:], time.Now().UnixMilli()).Scan(&name)
	return name, err
}

func newHumanToken() (string, [32]byte, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", [32]byte{}, err
	}
	token := humanSessionPrefix + base64.RawURLEncoding.EncodeToString(secret)
	return token, sha256.Sum256([]byte(token)), nil
}
