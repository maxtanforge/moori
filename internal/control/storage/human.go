package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"golang.org/x/crypto/scrypt"
)

const (
	humanSessionPrefix      = "moori_session_"
	humanPasswordLimit      = 5
	humanPasswordLockPeriod = 15 * time.Minute
	humanPasswordFields     = "id,username,salt,password_hash,password_locked_until_ms"
	humanPasswordUnchanged  = "id=? AND username=? AND salt=? AND password_hash=?"
	humanPasswordEligible   = humanPasswordUnchanged + " AND password_locked_until_ms<=?"
)

func normalizeUsername(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

func validUsername(username string) bool {
	if username == "" || len(username) > 64 {
		return false
	}
	for _, c := range username {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}

func hashPassword(password string, salt []byte) ([]byte, error) {
	return scrypt.Key([]byte(password), salt, 32768, 8, 3, 32)
}

func newPassword(password string) ([]byte, []byte, error) {
	if len(password) < 12 || len(password) > 1024 {
		return nil, nil, ErrInvalid
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, nil, err
	}
	digest, err := hashPassword(password, salt)
	return salt, digest, err
}

func (s *Store) CreateHumanUser(ctx context.Context, username, password string) (string, error) {
	username = normalizeUsername(username)
	if !validUsername(username) {
		return "", ErrInvalid
	}
	salt, digest, err := newPassword(password)
	if err != nil {
		return "", err
	}
	id := "human:" + rand.Text()
	if _, err := s.db.ExecContext(ctx, "INSERT INTO human_users(id,username,salt,password_hash) VALUES (?,?,?,?)", id, username, salt, digest); err != nil {
		return "", err
	}
	return id, nil
}

type humanPassword struct {
	id, username string
	salt, digest []byte
}

func (s *Store) verifyHuman(ctx context.Context, row *sql.Row, password string) (humanPassword, error) {
	record := humanPassword{salt: make([]byte, 16), digest: make([]byte, 32)}
	var lockedUntil int64
	err := row.Scan(&record.id, &record.username, &record.salt, &record.digest, &lockedUntil)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return humanPassword{}, err
	}
	if len(password) == 0 || len(password) > 1024 {
		return humanPassword{}, ErrForbidden
	}
	locked := lockedUntil > time.Now().UnixMilli()
	computed, hashErr := hashPassword(password, record.salt)
	if hashErr != nil {
		return humanPassword{}, hashErr
	}
	if locked {
		return humanPassword{}, ErrForbidden
	}
	if subtle.ConstantTimeCompare(computed, record.digest) != 1 || err != nil {
		if err == nil {
			if err := s.failHumanPassword(ctx, record); err != nil {
				return humanPassword{}, err
			}
		}
		return humanPassword{}, ErrForbidden
	}
	return record, nil
}

// The UPDATE serializes failures across service connections. Only the fifth
// consecutive failure records a lock; attempts during that period do not
// extend it. The first failure after expiry starts a new sequence.
func (s *Store) failHumanPassword(ctx context.Context, user humanPassword) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now()
	var lockedUntil int64
	err = tx.QueryRowContext(ctx, `UPDATE human_users SET
 password_failures=CASE WHEN password_locked_until_ms=0 THEN password_failures+1 ELSE 1 END,
 password_locked_until_ms=CASE WHEN password_locked_until_ms=0 AND password_failures=? THEN ? ELSE 0 END
 WHERE `+humanPasswordEligible+`
 RETURNING password_locked_until_ms`,
		humanPasswordLimit-1, now.Add(humanPasswordLockPeriod).UnixMilli(),
		user.id, user.username, user.salt, user.digest, now.UnixMilli()).Scan(&lockedUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if lockedUntil > 0 {
		if err := addProjectAudit(ctx, tx, "", "account.password_locked", "system", user.id, ""); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) LoginHuman(ctx context.Context, username, password string, ttl time.Duration) (string, error) {
	if ttl < time.Second || ttl > 24*time.Hour {
		return "", ErrInvalid
	}
	row := s.db.QueryRowContext(ctx, "SELECT "+humanPasswordFields+" FROM human_users WHERE username=?", normalizeUsername(username))
	user, err := s.verifyHuman(ctx, row, password)
	if err != nil {
		return "", err
	}
	token, hash, err := newHumanToken()
	if err != nil {
		return "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	now := time.Now()
	res, err := tx.ExecContext(ctx, `UPDATE human_users SET password_failures=0,password_locked_until_ms=0
 WHERE `+humanPasswordEligible+``,
		user.id, user.username, user.salt, user.digest, now.UnixMilli())
	if err := expectOne(res, err, ErrForbidden); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM human_sessions WHERE user_id=? AND expires_ms<=?", user.id, now.UnixMilli()); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO human_sessions(token_hash,user_id,expires_ms) VALUES(?,?,?)", hash[:], user.id, now.Add(ttl).UnixMilli()); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return token, nil
}

func humanTokenHash(token string) ([32]byte, error) {
	if len(token) != len(humanSessionPrefix)+base64.RawURLEncoding.EncodedLen(32) || !strings.HasPrefix(token, humanSessionPrefix) {
		return [32]byte{}, ErrForbidden
	}
	return sha256.Sum256([]byte(token)), nil
}

func (s *Store) AuthenticateHuman(ctx context.Context, token string) (string, error) {
	hash, err := humanTokenHash(token)
	if err != nil {
		return "", err
	}
	return findHumanSession(ctx, s.db, hash)
}

func findHumanSession(ctx context.Context, q queryRower, hash [32]byte) (string, error) {
	var id string
	err := q.QueryRowContext(ctx, "SELECT user_id FROM human_sessions WHERE token_hash=? AND expires_ms>?", hash[:], time.Now().UnixMilli()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrForbidden
	}
	return id, err
}

func (s *Store) LogoutHuman(ctx context.Context, token string) error {
	hash, err := humanTokenHash(token)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "DELETE FROM human_sessions WHERE token_hash=?", hash[:])
	return err
}

func (s *Store) ChangeHumanPassword(ctx context.Context, user, current, replacement string) error {
	if len(replacement) < 12 || len(replacement) > 1024 || replacement == current {
		return ErrInvalid
	}
	row := s.db.QueryRowContext(ctx, "SELECT "+humanPasswordFields+" FROM human_users WHERE id=?", user)
	old, err := s.verifyHuman(ctx, row, current)
	if err != nil {
		return err
	}
	salt, digest, err := newPassword(replacement)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE human_users SET salt=?,password_hash=?,password_failures=0,password_locked_until_ms=0
 WHERE `+humanPasswordEligible+``,
		salt, digest, old.id, old.username, old.salt, old.digest, time.Now().UnixMilli())
	if err := expectOne(res, err, ErrForbidden); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM human_sessions WHERE user_id=?", old.id); err != nil {
		return err
	}
	return tx.Commit()
}
