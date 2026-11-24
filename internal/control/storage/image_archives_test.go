package storage

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/core/nodeops"
)

func TestImageTransferBindsOwnerAndActiveNodeGeneration(t *testing.T) {
	ctx := t.Context()
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	owner, err := s.InitializeAdmin(ctx, "owner", "initial password", time.Hour)
	must(err)
	_, err = s.CreateHumanUser(ctx, "other", "other user password")
	must(err)
	must(s.SetPlatformAdmin(ctx, "other", true))
	other, err := s.LoginHuman(ctx, "other", "other user password", time.Hour)
	must(err)
	_, gen, err := s.IssueAgentCredential(ctx, "remote", time.Hour)
	must(err)
	now := time.Now()
	must(s.RecordNodeOperationCapability(ctx, "remote", gen, now))
	req := nodeops.Request{Action: nodeops.ImageExport, ImageID: "sha256:" + strings.Repeat("a", 64)}
	op, err := s.QueueNodeOperation(ctx, owner, "remote", gen, req, now)
	must(err)
	if action, err := s.HumanImageTransfer(ctx, owner, "remote", op.ID, now); err != nil || action != nodeops.ImageExport {
		t.Fatal("initiating administrator could not attach")
	}
	if _, err := s.HumanImageTransfer(ctx, other, "remote", op.ID, now); !errors.Is(err, ErrAccessDenied) {
		t.Fatal("different administrator could attach")
	}
	if _, err := s.HumanImageTransfer(ctx, owner, "different", op.ID, now); !errors.Is(err, ErrAccessDenied) {
		t.Fatal("operation reused on a different node")
	}
	must(s.RevokeAgentCredential(ctx, "remote"))
	if _, err := s.HumanImageTransfer(ctx, owner, "remote", op.ID, now); !errors.Is(err, ErrAccessDenied) {
		t.Fatal("revoked Agent's operation could still attach")
	}
}
