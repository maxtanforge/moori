package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"time"
)

const humanUserPageSize = 50

var (
	ErrHumanPasswordConfirmation = errors.New("administrator password confirmation failed")
	ErrHumanSelfReset            = errors.New("use account settings to change your own password")
)

type HumanUserIdentity struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

type HumanUserSummary struct {
	HumanUserIdentity
	PlatformAdmin bool       `json:"platform_admin"`
	LockedUntil   *time.Time `json:"locked_until,omitempty"`
}

func (s *Store) ListHumanUsers(ctx context.Context, token, after string) (CatalogPage[HumanUserSummary], error) {
	page := CatalogPage[HumanUserSummary]{Items: []HumanUserSummary{}}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	if _, err := platformActor(ctx, tx, token); err != nil {
		return page, err
	}
	if after != "" && !validUsername(after) {
		return page, ErrInvalid
	}

	now := time.Now().UnixMilli()
	rows, err := tx.QueryContext(ctx, `SELECT u.id,u.username,a.user_id IS NOT NULL,u.password_locked_until_ms
 FROM human_users u LEFT JOIN platform_admins a ON a.user_id=u.id
 WHERE u.username>? ORDER BY u.username LIMIT ?`, after, humanUserPageSize+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		if len(page.Items) == humanUserPageSize {
			page.Next = page.Items[humanUserPageSize-1].Username
			break
		}
		var user HumanUserSummary
		var lockedUntil int64
		if err := rows.Scan(&user.ID, &user.Username, &user.PlatformAdmin, &lockedUntil); err != nil {
			return page, err
		}
		if lockedUntil > now {
			deadline := time.UnixMilli(lockedUntil).UTC()
			user.LockedUntil = &deadline
		}
		page.Items = append(page.Items, user)
	}
	return page, rows.Err()
}

func (s *Store) CreateManagedHumanUser(ctx context.Context, token, username, password string) (HumanUserIdentity, error) {
	if _, err := platformActor(ctx, s.db, token); err != nil {
		return HumanUserIdentity{}, err
	}
	username = normalizeUsername(username)
	if !validUsername(username) {
		return HumanUserIdentity{}, ErrInvalid
	}
	salt, digest, err := newPassword(password)
	if err != nil {
		return HumanUserIdentity{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return HumanUserIdentity{}, err
	}
	defer tx.Rollback()
	actor, err := platformActor(ctx, tx, token)
	if err != nil {
		return HumanUserIdentity{}, err
	}
	id := "human:" + rand.Text()
	result, err := tx.ExecContext(ctx, `INSERT INTO human_users(id,username,salt,password_hash)
 VALUES(?,?,?,?) ON CONFLICT(username) DO NOTHING`, id, username, salt, digest)
	if err := expectOne(result, err, ErrUsernameTaken); err != nil {
		return HumanUserIdentity{}, err
	}
	if err := addProjectAudit(ctx, tx, "", "account.created", actor, id, ""); err != nil {
		return HumanUserIdentity{}, err
	}
	if err := tx.Commit(); err != nil {
		return HumanUserIdentity{}, err
	}
	return HumanUserIdentity{ID: id, Username: username}, nil
}

func (s *Store) ResetManagedHumanPassword(ctx context.Context, token, target, current, replacement string) error {
	actor, err := platformActor(ctx, s.db, token)
	if err != nil {
		return err
	}
	if target == actor {
		return ErrHumanSelfReset
	}
	if len(replacement) < 12 || len(replacement) > 1024 {
		return ErrInvalid
	}
	row := s.db.QueryRowContext(ctx, "SELECT "+humanPasswordFields+" FROM human_users WHERE id=?", actor)
	verified, err := s.verifyHuman(ctx, row, current)
	if errors.Is(err, ErrForbidden) {
		if _, err := platformActor(ctx, s.db, token); err != nil {
			return err
		}
		return ErrHumanPasswordConfirmation
	}
	if err != nil {
		return err
	}
	salt, digest, err := newPassword(replacement)
	if err != nil {
		return err
	}

	// SQLite starts the write transaction immediately. Session and grant
	// revalidation and the credential updates share the same write lock.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	liveActor, err := platformActor(ctx, tx, token)
	if err != nil {
		return err
	}
	if liveActor != actor {
		return ErrForbidden
	}
	result, err := tx.ExecContext(ctx, `UPDATE human_users SET password_failures=0,password_locked_until_ms=0
 WHERE `+humanPasswordEligible, verified.id, verified.username, verified.salt, verified.digest, time.Now().UnixMilli())
	if err := expectOne(result, err, ErrHumanPasswordConfirmation); err != nil {
		return err
	}
	result, err = tx.ExecContext(ctx, `UPDATE human_users SET salt=?,password_hash=?,password_failures=0,password_locked_until_ms=0
 WHERE id=?`, salt, digest, target)
	if err := expectOne(result, err, sql.ErrNoRows); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM human_sessions WHERE user_id=?", target); err != nil {
		return err
	}
	if err := addProjectAudit(ctx, tx, "", "account.password_reset", actor, target, ""); err != nil {
		return err
	}
	return tx.Commit()
}
