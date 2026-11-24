package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestTargetOnboardingAuthorizationRetryAndIsolation(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	admin, err := s.InitializeAdmin(ctx, "admin", "initial password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateHumanUser(ctx, "viewer", "viewer password"); err != nil {
		t.Fatal(err)
	}
	viewer, err := s.LoginHuman(ctx, "viewer", "viewer password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	in := TargetSetup{Project: "app", Target: "production", Agent: "server-2", Generation: 1}
	if err := s.ProvisionHumanTarget(ctx, viewer, in); !errors.Is(err, ErrAccessDenied) {
		t.Fatal(err)
	}
	if _, err := s.ListHumanAgents(ctx, viewer, ""); !errors.Is(err, ErrAccessDenied) {
		t.Fatal(err)
	}
	if err := s.ProvisionHumanTarget(ctx, admin, in); !errors.Is(err, ErrConflict) {
		t.Fatal("unregistered Agent accepted", err)
	}
	issued, err := s.CreateHumanAgentToken(ctx, admin, in.Agent, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ProvisionHumanTarget(ctx, admin, in); err != nil {
		t.Fatal(err)
	}
	if err := s.ProvisionHumanTarget(ctx, admin, in); err != nil {
		t.Fatal("retry", err)
	}
	id, g, err := s.AuthenticateAgentCredential(ctx, issued.Token)
	if err != nil || id != in.Agent || g != 1 {
		t.Fatal(id, g, err)
	}
	replacement := in
	replacement.Generation = 2
	if err := s.ProvisionHumanTarget(ctx, admin, replacement); !errors.Is(err, ErrConflict) {
		t.Fatal("reassigned existing target", err)
	}
	replacement.Target = "other"
	if err := s.ProvisionHumanTarget(ctx, admin, replacement); !errors.Is(err, ErrConflict) {
		t.Fatal("accepted invalid generation", err)
	}
	if _, _, err := s.AuthenticateAgentCredential(ctx, issued.Token); err != nil {
		t.Fatal(err)
	}
	existing := in
	existing.Target = "staging"
	if err := s.ProvisionHumanTarget(ctx, admin, existing); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeAgentCredential(ctx, in.Agent); err != nil {
		t.Fatal(err)
	}
	existing.Target = "revoked"
	if err := s.ProvisionHumanTarget(ctx, admin, existing); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	page, err := s.ListHumanAgents(ctx, admin, "")
	if err != nil || len(page.Items) != 0 {
		t.Fatal(page, err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM project_audit WHERE action='target.onboarded'`).Scan(&count); err != nil || count != 2 {
		t.Fatal("retry duplicated audit", count, err)
	}
}

func TestLocalTargetOnboardingAndAtomicRollback(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	admin, err := s.InitializeAdmin(ctx, "admin", "initial password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	in := TargetSetup{Project: "app", Target: "local-production", Local: true}
	if err := s.ProvisionHumanTarget(ctx, admin, in); !errors.Is(err, ErrConflict) {
		t.Fatal("missing executor accepted", err)
	}
	if err := s.EnsureLocalExecutor(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureLocalExecutor(ctx); err != nil {
		t.Fatal("restart changed identity", err)
	}
	if _, _, err := s.IssueAgentCredential(ctx, LocalExecutorID, time.Hour); !errors.Is(err, ErrInvalid) {
		t.Fatal("remote local identity allowed", err)
	}
	if err := s.ProvisionHumanTarget(ctx, admin, in); err != nil {
		t.Fatal(err)
	}
	if err := s.ProvisionHumanTarget(ctx, admin, in); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListHumanProjects(ctx, admin, "")
	if err != nil || len(page.Items) != 1 || page.Items[0].Role != "platform_admin" {
		t.Fatal(page, err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER fail_onboarding BEFORE INSERT ON project_audit BEGIN SELECT RAISE(ABORT,'fail'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateHumanAgentToken(ctx, admin, "bad", ""); err == nil {
		t.Fatal("token creation audit failure ignored")
	}
	failingTarget := TargetSetup{Project: "bad", Target: "bad", Local: true}
	if err := s.ProvisionHumanTarget(ctx, admin, failingTarget); err == nil {
		t.Fatal("target creation audit failure ignored")
	}
	var count int
	if err := s.db.QueryRow(`SELECT (SELECT count(*) FROM targets WHERE id='bad')+(SELECT count(*) FROM agent_certificates WHERE agent='bad')+(SELECT count(*) FROM agent_credentials WHERE agent='bad')`).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial commit", count, err)
	}
	if err := s.enterRecovery(ctx); err != nil {
		t.Fatal(err)
	}
	admin, err = s.LoginHuman(ctx, "admin", "initial password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	in.Target = "another"
	if err := s.ProvisionHumanTarget(ctx, admin, in); !errors.Is(err, ErrRecovery) {
		t.Fatal("recovery bypass", err)
	}
}
