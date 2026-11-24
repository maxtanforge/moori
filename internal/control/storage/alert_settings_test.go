package storage

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func platformSession(t *testing.T, ctx context.Context, s *Store, name string) string {
	t.Helper()
	if _, err := s.CreateHumanUser(ctx, name, name+" test password"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPlatformAdmin(ctx, name, true); err != nil {
		t.Fatal(err)
	}
	token, err := s.LoginHuman(ctx, name, name+" test password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestAlertSettingsRoundTripAndAccessControl(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	if _, ok, err := s.AlertSettings(ctx); err != nil || ok {
		t.Fatalf("unset settings ok=%v err=%v", ok, err)
	}
	admin := platformSession(t, ctx, s, "admin")
	settings := AlertSettings{CPUPercent: 95, MemoryPercent: 92, SustainedSeconds: 180}
	if err := s.SetAlertSettings(ctx, "", settings); err == nil {
		t.Fatal("empty session accepted")
	}
	if err := s.SetAlertSettings(ctx, admin, settings); err != nil {
		t.Fatal(err)
	}
	stored, ok, err := s.AlertSettings(ctx)
	if err != nil || !ok {
		t.Fatalf("stored ok=%v err=%v", ok, err)
	}
	if stored.CPUPercent != 95 || stored.MemoryPercent != 92 || stored.SustainedSeconds != 180 {
		t.Fatalf("stored = %+v", stored)
	}
	if stored.UpdatedBy != "admin" || stored.UpdatedAt.IsZero() {
		t.Fatalf("provenance = %+v", stored)
	}
	for _, broken := range []AlertSettings{
		{CPUPercent: 0, MemoryPercent: 90, SustainedSeconds: 120},
		{CPUPercent: 101, MemoryPercent: 90, SustainedSeconds: 120},
		{CPUPercent: 90, MemoryPercent: 0, SustainedSeconds: 120},
		{CPUPercent: 90, MemoryPercent: 101, SustainedSeconds: 120},
		{CPUPercent: 90, MemoryPercent: 90, SustainedSeconds: 0},
		{CPUPercent: 90, MemoryPercent: 90, SustainedSeconds: 3601},
	} {
		if err := s.SetAlertSettings(ctx, admin, broken); !errors.Is(err, ErrInvalid) {
			t.Fatalf("broken %+v accepted: %v", broken, err)
		}
	}
	if err := s.SetAlertSettings(ctx, admin, AlertSettings{CPUPercent: 80, MemoryPercent: 85, SustainedSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearAlertSettings(ctx, admin); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.AlertSettings(ctx); err != nil || ok {
		t.Fatalf("cleared settings ok=%v err=%v", ok, err)
	}
	if err := s.ClearAlertSettings(ctx, admin); err != nil {
		t.Fatalf("second clear = %v", err)
	}
}

func TestAlertTargetLifecycleAndLimits(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	admin := platformSession(t, ctx, s, "admin")
	if targets, err := s.AlertNotificationTargets(ctx); err != nil || len(targets) != 0 {
		t.Fatalf("initial targets = %v, err = %v", targets, err)
	}
	for _, invalid := range []string{
		"",
		"not a url",
		"ftp://hooks.example.com/x",
		"https://user:pass@hooks.example.com/x",
		"https://hooks.example.com/x#fragment",
		"https://hooks.example.com/with space",
		strings.Repeat("https://hooks.example.com/", 30),
	} {
		if err := ValidateAlertTarget(invalid); err == nil {
			t.Fatalf("invalid URL accepted: %q", invalid)
		}
		if _, err := s.CreateAlertTarget(ctx, admin, invalid); !errors.Is(err, ErrInvalid) {
			t.Fatalf("create %q = %v", invalid, err)
		}
	}
	valid := "https://hooks.example.com/alert?token=secret"
	target, err := s.CreateAlertTarget(ctx, admin, "  "+valid+"  ")
	if err != nil {
		t.Fatal(err)
	}
	if target.ID == "" || target.URL != valid || target.CreatedBy != "admin" || target.CreatedAt.IsZero() {
		t.Fatalf("target = %+v", target)
	}
	listed, err := s.AlertTargets(ctx, admin)
	if err != nil || len(listed) != 1 || listed[0].ID != target.ID {
		t.Fatalf("listed = %+v, err = %v", listed, err)
	}
	urls, err := s.AlertNotificationTargets(ctx)
	if err != nil || len(urls) != 1 || urls[0] != valid {
		t.Fatalf("notification targets = %v, err = %v", urls, err)
	}
	// Audit keeps the token-bearing path out of the log.
	sameOrigin := auditTargetDetail(valid)
	if strings.Contains(sameOrigin, "token") || !strings.HasPrefix(sameOrigin, "https://hooks.example.com") {
		t.Fatalf("audit detail = %q", sameOrigin)
	}
	if err := s.DeleteAlertTarget(ctx, admin, "alert-missing"); err == nil {
		t.Fatal("missing target delete succeeded")
	}
	if err := s.DeleteAlertTarget(ctx, admin, target.ID); err != nil {
		t.Fatal(err)
	}
	if targets, err := s.AlertNotificationTargets(ctx); err != nil || len(targets) != 0 {
		t.Fatalf("post-delete targets = %v, err = %v", targets, err)
	}
}

func TestAlertTargetsAreCappedAndEnvSeedRunsOnce(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	admin := platformSession(t, ctx, s, "admin")
	imported, err := s.EnsureAlertTarget(ctx, "https://env.example.com/hook")
	if err != nil || !imported {
		t.Fatalf("seed imported=%v err=%v", imported, err)
	}
	if imported, err := s.EnsureAlertTarget(ctx, "https://env.example.com/other"); err != nil || imported {
		t.Fatalf("second seed imported=%v err=%v", imported, err)
	}
	targets, err := s.AlertNotificationTargets(ctx)
	if err != nil || len(targets) != 1 || targets[0] != "https://env.example.com/hook" {
		t.Fatalf("seeded targets = %v, err = %v", targets, err)
	}
	// The imported row has no human account; listing must not fail on the
	// missing username join.
	listed, err := s.AlertTargets(ctx, admin)
	if err != nil || len(listed) != 1 || listed[0].CreatedBy != "environment" {
		t.Fatalf("listed imported target = %+v, err = %v", listed, err)
	}
	if _, err := s.EnsureAlertTarget(ctx, ""); err != nil {
		t.Fatalf("empty seed = %v", err)
	}
	for i := range maxAlertTargets - 1 {
		host := "https://hooks" + string(rune('a'+i)) + ".example.com/x"
		if _, err := s.CreateAlertTarget(ctx, admin, host); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.CreateAlertTarget(ctx, admin, "https://overflow.example.com/x"); !errors.Is(err, ErrConflict) {
		t.Fatalf("overflow create = %v", err)
	}
}

func TestAlertSettingsDenyNonAdmins(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	if _, err := s.CreateHumanUser(ctx, "member", "member test password"); err != nil {
		t.Fatal(err)
	}
	member, err := s.LoginHuman(ctx, "member", "member test password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetAlertSettings(ctx, member, AlertSettings{CPUPercent: 95, MemoryPercent: 95, SustainedSeconds: 120}); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("member set = %v", err)
	}
	if err := s.ClearAlertSettings(ctx, member); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("member clear = %v", err)
	}
	if _, err := s.CreateAlertTarget(ctx, member, "https://hooks.example.com/x"); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("member create = %v", err)
	}
	if _, err := s.AlertTargets(ctx, member); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("member list = %v", err)
	}
	if err := s.DeleteAlertTarget(ctx, member, "alert-x"); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("member delete = %v", err)
	}
	if targets, err := s.AlertNotificationTargets(ctx); err != nil || len(targets) != 0 {
		t.Fatalf("no target should exist = %v, err = %v", targets, err)
	}
}
