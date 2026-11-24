package storage

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestManagedHumanUsersPageByUsername(t *testing.T) {
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	ctx := t.Context()
	admin, err := s.InitializeAdmin(ctx, "admin", "initial password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for i := 54; i >= 0; i-- {
		username := fmt.Sprintf("person-%02d", i)
		if _, err := s.db.ExecContext(ctx, "INSERT INTO human_users(id,username,salt,password_hash) VALUES(?,?,zeroblob(16),zeroblob(32))",
			"test:"+username, username); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.ListHumanUsers(ctx, admin, "")
	if err != nil || len(first.Items) != 50 || first.Next != "person-48" {
		t.Fatalf("first page has %d users, cursor %q and error %v", len(first.Items), first.Next, err)
	}
	second, err := s.ListHumanUsers(ctx, admin, first.Next)
	if err != nil || len(second.Items) != 6 || second.Next != "" {
		t.Fatalf("second page has %d users, cursor %q and error %v", len(second.Items), second.Next, err)
	}
	expected := make([]string, 0, 56)
	expected = append(expected, "admin")
	for i := range 55 {
		expected = append(expected, fmt.Sprintf("person-%02d", i))
	}
	for i, record := range append(first.Items, second.Items...) {
		if record.Username != expected[i] || record.PlatformAdmin != (i == 0) || record.LockedUntil != nil {
			t.Fatalf("unexpected account at %d: %+v", i, record)
		}
	}
}

func TestManagedHumanPasswordResetRequiresEligibleAdminAndAuditsSuccess(t *testing.T) {
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	ctx := t.Context()
	const actorUsername = "admin"
	const actorPassword = "initial password"
	const targetPassword = "target initial password"
	const replacement = "target replacement password"

	token, err := s.InitializeAdmin(ctx, actorUsername, actorPassword, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	actor, err := s.AuthenticateHuman(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	target, err := s.CreateManagedHumanUser(ctx, token, "other-admin", targetPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "INSERT INTO platform_admins(user_id) VALUES(?)", target.ID); err != nil {
		t.Fatal(err)
	}
	otherToken, err := s.LoginHuman(ctx, target.Username, targetPassword, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	for range humanPasswordLimit {
		if err := s.ResetManagedHumanPassword(ctx, token, target.ID, "wrong admin password", replacement); !errors.Is(err, ErrHumanPasswordConfirmation) {
			t.Fatalf("incorrect confirmation: %v", err)
		}
	}
	if err := s.ResetManagedHumanPassword(ctx, token, target.ID, actorPassword, replacement); !errors.Is(err, ErrHumanPasswordConfirmation) {
		t.Fatalf("locked actor with correct password: %v", err)
	}
	if _, err := s.AuthenticateHuman(ctx, otherToken); err != nil {
		t.Fatalf("rejected reset affected the target session: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE human_users SET password_locked_until_ms=? WHERE id=?",
		time.Now().Add(-time.Second).UnixMilli(), actor); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetManagedHumanPassword(ctx, token, target.ID, actorPassword, replacement); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateHuman(ctx, otherToken); !errors.Is(err, ErrForbidden) {
		t.Fatalf("target admin's old session: %v", err)
	}
	resetToken, err := s.LoginHuman(ctx, target.Username, replacement, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if granted, err := s.HumanPlatformAdmin(ctx, resetToken); err != nil || !granted {
		t.Fatalf("target admin grant: %t %v", granted, err)
	}
	audit, err := s.ReadProjectAudit(ctx, token, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	found := make(map[string]int)
	for _, event := range audit.Items {
		if event.Action != "account.created" && event.Action != "account.password_reset" {
			continue
		}
		if event.Actor != actor || event.Subject != target.ID || event.Detail != "" {
			t.Fatalf("unsafe or incorrect event: %+v", event)
		}
		found[event.Action]++
	}
	if found["account.created"] != 1 || found["account.password_reset"] != 1 {
		t.Fatalf("unexpected audit counts: %v", found)
	}
}
