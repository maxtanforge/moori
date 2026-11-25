package storage

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHumanSessionsAreRevokedByPasswordChange(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	user, err := s.CreateHumanUser(ctx, "member", "initial test password")
	if err != nil || !strings.HasPrefix(user, "human:") {
		t.Fatalf("user ID: %q %v", user, err)
	}
	if _, err := s.LoginHuman(ctx, "member", "wrong password", time.Hour); !errors.Is(err, ErrForbidden) {
		t.Fatalf("invalid login: %v", err)
	}
	token, err := s.LoginHuman(ctx, "member", "initial test password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.AuthenticateHuman(ctx, token); err != nil || got != user {
		t.Fatalf("session: %q %v", got, err)
	}
	if err := s.ChangeHumanPassword(ctx, user, "initial test password", "changed test password"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateHuman(ctx, token); !errors.Is(err, ErrForbidden) {
		t.Fatalf("old session still valid: %v", err)
	}
	if _, err := s.LoginHuman(ctx, "member", "initial test password", time.Hour); !errors.Is(err, ErrForbidden) {
		t.Fatalf("old password still valid: %v", err)
	}
	token, err = s.LoginHuman(ctx, "member", "changed test password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LogoutHuman(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateHuman(ctx, token); !errors.Is(err, ErrForbidden) {
		t.Fatalf("logout did not revoke session: %v", err)
	}
}

func TestHumanPasswordLockSurvivesConcurrentAttemptsAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")
	first := openTestStore(t, path)
	second := openTestStore(t, path)
	const username = "operator"
	const password = "operator initial password"
	id, err := first.CreateHumanUser(ctx, username, password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.CreateHumanUser(ctx, "other", password); err != nil {
		t.Fatal(err)
	}

	// A successful login clears an incomplete sequence.
	for range 4 {
		expectWrongHumanPassword(t, first, " OPERATOR ")
	}
	if _, err := first.LoginHuman(ctx, username, password, time.Hour); err != nil {
		t.Fatalf("account locked before five consecutive failures: %v", err)
	}

	attempts := make(chan error, 5)
	for i := range 5 {
		store := []*Store{first, second}[i%2]
		go func() {
			_, err := store.LoginHuman(ctx, username, "wrong password", time.Hour)
			attempts <- err
		}()
	}
	for range 5 {
		if err := <-attempts; !errors.Is(err, ErrForbidden) {
			t.Fatalf("wrong password returned %v", err)
		}
	}
	if _, err := second.LoginHuman(ctx, username, password, time.Hour); !errors.Is(err, ErrForbidden) {
		t.Fatalf("correct password was accepted after five failures: %v", err)
	}
	if _, err := second.LoginHuman(ctx, "other", password, time.Hour); err != nil {
		t.Fatalf("lock affected a different account: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, path)
	if _, err := reopened.LoginHuman(ctx, username, password, time.Hour); !errors.Is(err, ErrForbidden) {
		t.Fatalf("account unlocked on restart: %v", err)
	}

	var deadline, afterRetry int64
	readDeadline := func(dest *int64) {
		t.Helper()
		if err := reopened.db.QueryRowContext(ctx, "SELECT password_locked_until_ms FROM human_users WHERE id=?", id).Scan(dest); err != nil {
			t.Fatal(err)
		}
	}
	readDeadline(&deadline)
	if ttl := time.Until(time.UnixMilli(deadline)); ttl < 14*time.Minute || ttl > 15*time.Minute {
		t.Fatalf("unexpected lock duration: %v", ttl)
	}
	expectWrongHumanPassword(t, reopened, username)
	readDeadline(&afterRetry)
	if afterRetry != deadline {
		t.Fatal("an attempt during lock extended the deadline")
	}
	if _, err := reopened.db.ExecContext(ctx, "UPDATE human_users SET password_locked_until_ms=? WHERE id=?", time.Now().Add(-time.Second).UnixMilli(), id); err != nil {
		t.Fatal(err)
	}
	expectWrongHumanPassword(t, reopened, username)
	if _, err := reopened.LoginHuman(ctx, username, password, time.Hour); err != nil {
		t.Fatalf("expired lock was reused after a new failure: %v", err)
	}
}

func TestHumanPasswordChecksShareFailureCountAndResetOnChange(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	const username = "operator"
	const password = "operator initial password"
	const replacement = "operator replacement password"
	id, err := s.CreateHumanUser(ctx, username, password)
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.LoginHuman(ctx, username, password, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for range 4 {
		expectWrongHumanPassword(t, s, username)
	}
	if err := s.ChangeHumanAccount(ctx, token, username, password, replacement); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateHuman(ctx, token); !errors.Is(err, ErrForbidden) {
		t.Fatalf("password change preserved old session: %v", err)
	}
	expectWrongHumanPassword(t, s, username)
	token, err = s.LoginHuman(ctx, username, replacement, time.Hour)
	if err != nil {
		t.Fatalf("password change did not clear failures: %v", err)
	}
	for range 3 {
		expectWrongHumanPassword(t, s, username)
	}
	if err := s.ChangeHumanAccount(ctx, token, username, "wrong password", replacement); !errors.Is(err, ErrForbidden) {
		t.Fatalf("wrong account password: %v", err)
	}
	if err := s.ChangeHumanPassword(ctx, id, "wrong password", password); !errors.Is(err, ErrForbidden) {
		t.Fatalf("wrong current password: %v", err)
	}
	if err := s.ChangeHumanAccount(ctx, token, username, replacement, password); !errors.Is(err, ErrForbidden) {
		t.Fatalf("locked account password accepted: %v", err)
	}
	if _, err := s.LoginHuman(ctx, username, replacement, time.Hour); !errors.Is(err, ErrForbidden) {
		t.Fatalf("current password failures did not lock the login: %v", err)
	}
	if _, err := s.AuthenticateHuman(ctx, token); err != nil {
		t.Fatalf("login lock invalidated an existing session: %v", err)
	}
}

func TestLockedHumanAccountReturnsUniformReauthenticationError(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	const username = "operator"
	const password = "operator initial password"
	const existing = "already-taken"
	if _, err := s.CreateHumanUser(ctx, username, password); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateHumanUser(ctx, existing, "other initial password"); err != nil {
		t.Fatal(err)
	}
	token, err := s.LoginHuman(ctx, username, password, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		expectWrongHumanPassword(t, s, username)
	}
	for _, tc := range []struct{ label, name, replacement string }{
		{"taken username", existing, ""},
		{"invalid replacement", username, "short"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			if err := s.ChangeHumanAccount(ctx, token, tc.name, password, tc.replacement); !errors.Is(err, ErrForbidden) {
				t.Fatalf("locked password check exposed a validation result: %v", err)
			}
		})
	}
}

func expectWrongHumanPassword(t *testing.T, s *Store, username string) {
	t.Helper()
	if _, err := s.LoginHuman(context.Background(), username, "wrong password", time.Hour); !errors.Is(err, ErrForbidden) {
		t.Fatalf("wrong password returned %v", err)
	}
}

func TestInvalidHumanPasswordLengthDoesNotKeepDatabaseConnection(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	if _, err := s.CreateHumanUser(ctx, "operator", "operator initial password"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoginHuman(ctx, "operator", "", time.Hour); !errors.Is(err, ErrForbidden) {
		t.Fatalf("empty password returned %v", err)
	}
	// The store uses a single SQLite connection.
	queryCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if _, err := s.SetupRequired(queryCtx); err != nil {
		t.Fatalf("password check held the database connection: %v", err)
	}
}
