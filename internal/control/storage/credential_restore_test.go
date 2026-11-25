package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestRestoreReissuesCredentialOnceWithoutLosingTargetGeneration(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "revoked-after-backup", true: "expired-backup"}[expired], func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			live, backup, recovery := filepath.Join(dir, "live.db"), filepath.Join(dir, "backup.db"), filepath.Join(dir, "recovery.db")
			s := openTestStore(t, live)
			old, g, err := s.IssueAgentCredential(ctx, "node", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			for _, target := range []string{"target", "held"} {
				if err := s.RegisterAssignedTarget(ctx, "project", target, "node", g); err != nil {
					t.Fatal(err)
				}
			}
			otherToken, otherGeneration, err := s.IssueAgentCredential(ctx, "other-node", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.RegisterAssignedTarget(ctx, "project", "other-target", "other-node", otherGeneration); err != nil {
				t.Fatal(err)
			}
			if expired {
				if _, err := s.db.Exec("UPDATE agent_credentials SET expires_ms=0"); err != nil {
					t.Fatal(err)
				}
			}
			if err := BackupDatabase(ctx, live, backup); err != nil {
				t.Fatal(err)
			}
			if err := s.RevokeAgentCredential(ctx, "node"); err != nil {
				t.Fatal(err)
			}
			if err := PrepareRestore(ctx, backup, recovery); err != nil {
				t.Fatal(err)
			}
			restored := openTestStore(t, recovery)
			if _, _, err := restored.AuthenticateAgentCredential(ctx, old); !errors.Is(err, ErrForbidden) {
				t.Fatalf("backup credential revived: %v", err)
			}
			if err := restored.ValidateAgent(ctx, "node", g); err == nil {
				t.Fatal("pending recovery session accepted")
			}
			token, newGeneration, err := restored.IssueAgentCredential(ctx, "node", time.Hour)
			if err != nil || newGeneration != g {
				t.Fatalf("recovery reissue changed generation: %d want %d, %v", newGeneration, g, err)
			}
			if _, got, err := restored.AuthenticateAgentCredential(ctx, token); err != nil || got != g {
				t.Fatalf("replacement authentication: %d %v", got, err)
			}
			if _, _, err := restored.AuthenticateAgentCredential(ctx, old); !errors.Is(err, ErrForbidden) {
				t.Fatal("old token accepted after reissue", err)
			}
			if _, _, err := restored.AuthenticateAgentCredential(ctx, otherToken); !errors.Is(err, ErrForbidden) {
				t.Fatal("other Agent revived", err)
			}
			if err := restored.ValidateAgent(ctx, "other-node", otherGeneration); err == nil {
				t.Fatal("other Agent unlocked by reissue")
			}
			epoch, err := restored.RecoveryEpoch(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := restored.ReleaseRestoredTarget(ctx, epoch, "project", "target", "operator", "Docker inspected"); !errors.Is(err, ErrRecovery) {
				t.Fatalf("released without inventory: %v", err)
			}
			if err := restored.RecordInventory(ctx, epoch, "node", g, nil, true); err != nil {
				t.Fatal(err)
			}
			if err := restored.ReleaseRestoredTarget(ctx, epoch, "project", "target", "operator", "Docker inspected"); err != nil {
				t.Fatal(err)
			}
			status, err := restored.RecoveryStatus(ctx)
			if err != nil || status.ReleasedTargets != 1 {
				t.Fatalf("unrelated target fence: %+v %v", status, err)
			}
			for i := int64(1); i <= 2; i++ {
				_, next, err := restored.IssueAgentCredential(ctx, "node", time.Hour)
				if err != nil || next != g+i {
					t.Fatalf("normal rotation %d: %d %v", i, next, err)
				}
			}
			if _, _, err := restored.AuthenticateAgentCredential(ctx, token); !errors.Is(err, ErrForbidden) {
				t.Fatal("replacement token survived rotation", err)
			}
		})
	}
}
func TestRecoveryCredentialReissueRequiresMatchingRegistryAndEpoch(t *testing.T) {
	for _, broken := range []string{"epoch", "fingerprint", "generation"} {
		t.Run(broken, func(t *testing.T) {
			ctx := context.Background()
			s := openTestStore(t, filepath.Join(t.TempDir(), "db"))
			old, g, err := s.IssueAgentCredential(ctx, "node", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.enterRecovery(ctx); err != nil {
				t.Fatal(err)
			}
			statement := map[string]string{"epoch": "DELETE FROM recovery_state", "fingerprint": "UPDATE agent_certificates SET fingerprint=zeroblob(32)", "generation": "UPDATE agent_credentials SET generation=generation+1"}[broken]
			if _, err := s.db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			_, next, err := s.IssueAgentCredential(ctx, "node", time.Hour)
			if err != nil || next != g+1 {
				t.Fatalf("unsafe pending state reused generation: %d %v", next, err)
			}
			if _, _, err := s.AuthenticateAgentCredential(ctx, old); !errors.Is(err, ErrForbidden) {
				t.Fatal("old credential accepted", err)
			}
		})
	}
}
