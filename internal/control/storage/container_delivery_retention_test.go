package storage

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/core/nodeops"
)

func TestContainerDeliveryReceiptsSurviveOperationCleanup(t *testing.T) {
	ctx := t.Context()
	s := openTestStore(t, filepath.Join(t.TempDir(), "moori.db"))
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	later := now.Add(31 * 24 * time.Hour)
	const repository = "registry.example/team/app"
	image := repository + "@sha256:" + strings.Repeat("a", 64)
	must(s.EnsureLocalExecutor(ctx))
	must(s.RecordNodeOperationCapability(ctx, LocalExecutorID, 1, now))
	must(s.RegisterContainerTarget(ctx, "team", "production", LocalExecutorID, 1, "ci-app", repository, ""))
	token, err := s.IssueServiceCredential(ctx, "ci", "team", "production", later.Add(time.Hour))
	must(err)
	admin, err := s.InitializeAdmin(ctx, "admin", "initial password", time.Hour)
	must(err)
	hook, err := s.CreateContainerWebhook(ctx, admin, ContainerWebhookBinding{
		Node: LocalExecutorID, Generation: 1, Container: "hook-app", Repository: repository,
	})
	must(err)
	ci, err := s.QueueCIContainerDeployment(ctx, token, "team", "production", "ci-old", image, now)
	must(err)
	webhook, err := s.QueueContainerWebhook(ctx, hook.ID, hook.Token, "hook-old", image, now)
	must(err)
	for _, id := range []string{ci.ID, webhook.ID} {
		_, err := s.db.ExecContext(ctx, "UPDATE node_operations SET state='succeeded',result='{}' WHERE id=?", id)
		must(err)
	}
	must(s.RecordNodeOperationCapability(ctx, LocalExecutorID, 1, later))
	_, err = s.QueueCIContainerDeployment(ctx, token, "team", "production", "ci-new", image, later)
	must(err)
	replay, err := s.QueueCIContainerDeployment(ctx, token, "team", "production", "ci-old", image, later)
	must(err)
	if replay.ID != ci.ID || replay.State != nodeops.Succeeded {
		t.Fatalf("CI replay: %+v", replay)
	}
	replay, err = s.QueueContainerWebhook(ctx, hook.ID, hook.Token, "hook-old", image, later)
	must(err)
	if replay.ID != webhook.ID || replay.State != nodeops.Succeeded {
		t.Fatalf("webhook replay: %+v", replay)
	}
	var outputs int
	must(s.db.QueryRowContext(ctx, "SELECT count(*) FROM node_operations WHERE id IN (?,?) AND result IS NOT NULL", ci.ID, webhook.ID).Scan(&outputs))
	if outputs != 0 {
		t.Fatal("expired output was retained with the receipts")
	}
}
