package storage

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestInitializeAdminOnceAcrossConnections(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")
	stores := []*Store{openTestStore(t, path), openTestStore(t, path)}
	var wg sync.WaitGroup
	tokens := make([]string, 2)
	errs := make([]error, 2)
	for i, s := range stores {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tokens[i], errs[i] = s.InitializeAdmin(ctx, "admin", "initial password", time.Hour)
		}()
	}
	wg.Wait()
	wins := 0
	for i, err := range errs {
		if err == nil {
			wins++
			if _, err := stores[i].AuthenticateHuman(ctx, tokens[i]); err != nil {
				t.Fatal(err)
			}
		} else if !errors.Is(err, ErrInitialized) {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatalf("setup winners=%d", wins)
	}
	var grants int
	if err := stores[0].db.QueryRow(`SELECT count(*) FROM platform_admins`).Scan(&grants); err != nil || grants != 1 {
		t.Fatalf("grants=%d err=%v", grants, err)
	}
	// Removing a grant is not permission to reopen anonymous installation.
	if err := stores[0].SetPlatformAdmin(ctx, "admin", false); err != nil {
		t.Fatal(err)
	}
	if required, err := stores[0].SetupRequired(ctx); err != nil || required {
		t.Fatal(required, err)
	}
}

func TestSetupAtomicAndExistingUserProtected(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	if _, err := s.InitializeAdmin(ctx, "admin", "short", time.Hour); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_setup BEFORE INSERT ON platform_admins BEGIN SELECT RAISE(ABORT,'fail'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InitializeAdmin(ctx, "admin", "initial password", time.Hour); err == nil {
		t.Fatal("grant failure ignored")
	}
	if required, err := s.SetupRequired(ctx); err != nil || !required {
		t.Fatal("partial setup committed", required, err)
	}
	if _, err := s.CreateHumanUser(ctx, "existing", "existing password"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InitializeAdmin(ctx, "admin", "initial password", time.Hour); !errors.Is(err, ErrInitialized) {
		t.Fatal("existing installation taken over", err)
	}
}

func TestAccountChangesRequirePasswordAndInvalidateSessions(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	token, err := s.InitializeAdmin(ctx, "admin", "initial password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.AuthenticateHuman(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateHumanUser(ctx, "other", "another password"); err != nil {
		t.Fatal(err)
	}
	if err := s.ChangeHumanAccount(ctx, token, "renamed", "wrong", ""); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if err := s.ChangeHumanAccount(ctx, token, "other", "initial password", ""); !errors.Is(err, ErrUsernameTaken) {
		t.Fatal(err)
	}
	if err := s.ChangeHumanAccount(ctx, token, "renamed", "initial password", "changed password"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateHuman(ctx, token); !errors.Is(err, ErrForbidden) {
		t.Fatal("old session valid", err)
	}
	if _, err := s.LoginHuman(ctx, "admin", "initial password", time.Hour); !errors.Is(err, ErrForbidden) {
		t.Fatal("old credentials valid", err)
	}
	fresh, err := s.LoginHuman(ctx, "renamed", "changed password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	newID, err := s.AuthenticateHuman(ctx, fresh)
	if err != nil || newID != id {
		t.Fatal("identity changed", err)
	}
	if err := s.ChangeHumanAccount(ctx, fresh, "renamed-again", "changed password", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoginHuman(ctx, "renamed-again", "changed password", time.Hour); err != nil {
		t.Fatal("rename lost password", err)
	}
}
