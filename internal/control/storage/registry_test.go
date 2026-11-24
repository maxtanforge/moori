package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/core/nodeops"
	"github.com/maxtanforge/moori/internal/core/registry"
	"github.com/maxtanforge/moori/internal/core/release"
)

func TestRegistryCredentialsFollowExecutionScope(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	admin, err := s.InitializeAdmin(ctx, "admin", "initial password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, generation, err := s.IssueAgentCredential(ctx, "agent", time.Hour); err != nil || generation != 1 {
		t.Fatal(err)
	}
	if err := s.RegisterAssignedTarget(ctx, "team", "target", "agent", 1); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureLocalExecutor(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterAssignedTarget(ctx, "team", "local", LocalExecutorID, 1); err != nil {
		t.Fatal(err)
	}
	const image = "registry.example/team/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const other = "registry.example/other/app@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	artifact, err := release.Compile(ctx, []byte("services: {web: {image: '"+image+"'}, other: {image: '"+other+"'}}"), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PublishRelease(ctx, "team", "r1", artifact); err != nil {
		t.Fatal(err)
	}
	key := SecretKey{23}
	input := RegistryInput{Name: "Shared", Host: "registry.example", RepositoryPrefix: "team", Username: "test-user", Password: "test-only-password"}
	entry, err := s.CreateHumanRegistry(ctx, admin, key, input)
	if err != nil {
		t.Fatal(err)
	}
	if !entry.HasCredential {
		t.Fatal("missing credential status")
	}
	var stored []byte
	if err := s.db.QueryRow("SELECT ciphertext FROM registries").Scan(&stored); err != nil || len(stored) == 0 || bytes.Contains(stored, []byte(input.Password)) {
		t.Fatalf("plaintext was stored or credential missing: %v", err)
	}
	page, err := s.ListHumanRegistries(ctx, admin, "")
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	serialized, err := json.Marshal(page)
	if err != nil || bytes.Contains(serialized, []byte(input.Password)) || bytes.Contains(serialized, []byte(input.Username)) {
		t.Fatal("metadata contains private credential")
	}
	for _, node := range []struct{ agent, target string }{{"agent", "target"}, {LocalExecutorID, "local"}} {
		token, err := s.IssueServiceCredential(ctx, "ci-"+node.target, "team", node.target, time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.AdmitPublishedRelease(ctx, token, Admission{ID: "deployment-" + node.target, Project: "team", Target: node.target, Key: "one", Release: "r1"}); err != nil {
			t.Fatal(err)
		}
		offer, err := s.NextOffer(ctx, node.agent, 1, time.Now(), time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.ExecutionRegistry(ctx, key, node.agent, 2, offer.Execution, offer.Digest, true); !errors.Is(err, ErrForbidden) {
			t.Fatalf("unassigned generation: %v", err)
		}
		if _, err := s.ExecutionRegistry(ctx, key, node.agent, 1, offer.Execution, offer.Digest, false); !errors.Is(err, registry.ErrSecureTransport) {
			t.Fatalf("insecure credential delivery: %v", err)
		}
		bundle, err := s.ExecutionRegistry(ctx, key, node.agent, 1, offer.Execution, offer.Digest, true)
		if err != nil || len(bundle) != 1 || bundle[image].Password != input.Password {
			t.Fatalf("scoped bundle mismatch: %v", err)
		}
		if node.agent != LocalExecutorID {
			if err := s.RevokeAgentCredential(ctx, node.agent); err != nil {
				t.Fatal(err)
			}
			if bundle, err := s.ExecutionRegistry(ctx, key, node.agent, 1, offer.Execution, offer.Digest, true); !errors.Is(err, ErrForbidden) || len(bundle) != 0 {
				t.Fatalf("revoked Agent still received registry credentials: %v", err)
			}
		}
	}
	otherRegistry, err := s.CreateHumanRegistry(ctx, admin, key, RegistryInput{Name: "Overlap", Host: "registry.example", RepositoryPrefix: "team/app"})
	if err != nil {
		t.Fatal(err)
	}
	checkSelection := func(repository, requested, want string, wantErr error) {
		t.Helper()
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		image, err := registry.ParseImage(repository)
		if err != nil {
			t.Fatal(err)
		}
		selected, err := selectRegistry(ctx, tx, requested, image)
		if selected.id != want || !errors.Is(err, wantErr) {
			t.Fatalf("%s with registry %q selected %q (%v), want %q (%v)", repository, requested, selected.id, err, want, wantErr)
		}
	}
	checkSelection(image, "", otherRegistry.ID, nil)
	checkSelection("registry.example/team/application:v1", "", entry.ID, nil)
	checkSelection("registry.example/teamwork/app:v1", "", "", nil)
	checkSelection("other.example/team/app:v1", entry.ID, "", registry.ErrDenied)
	checkSelection(image, entry.ID, entry.ID, nil)
	duplicate, err := s.CreateHumanRegistry(ctx, admin, key, RegistryInput{Name: "Duplicate", Host: "registry.example", RepositoryPrefix: "team/app"})
	if err != nil {
		t.Fatal(err)
	}
	checkSelection(image, "", "", registry.ErrAmbiguous)
	checkSelection(image, duplicate.ID, duplicate.ID, nil)
	if err := s.SetHumanRegistryEnabled(ctx, admin, duplicate.ID, false); err != nil {
		t.Fatal(err)
	}
	checkSelection(image, "", otherRegistry.ID, nil)
	checkSelection(image, duplicate.ID, "", registry.ErrDenied)
	if err := s.SetHumanRegistryEnabled(ctx, admin, entry.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := s.AuthorizeRegistryOperation(ctx, admin, "agent", "team", "target", entry.ID, image); !errors.Is(err, registry.ErrDenied) {
		t.Fatalf("disabled registry remained accessible: %v", err)
	}
}

func TestRegistryVerificationCredentialsFollowClaimedOperation(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	admin, err := s.InitializeAdmin(ctx, "admin", "initial password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureLocalExecutor(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterAssignedTarget(ctx, "team", "prod", LocalExecutorID, 1); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := s.RecordNodeOperationCapability(ctx, LocalExecutorID, 1, now); err != nil {
		t.Fatal(err)
	}
	key := SecretKey{29}
	item, err := s.CreateHumanRegistry(ctx, admin, key, RegistryInput{
		Name: "Shared", Host: "registry.example", RepositoryPrefix: "team", Username: "robot", Password: "test-initial",
	})
	if err != nil {
		t.Fatal(err)
	}
	req := nodeops.Request{Action: nodeops.RegistryVerify, RegistryID: item.ID, Project: "team", Target: "prod", Image: "registry.example/team/app:v1"}
	if err := s.AuthorizeRegistryOperation(ctx, admin, LocalExecutorID, req.Project, req.Target, req.RegistryID, req.Image); err != nil {
		t.Fatal(err)
	}
	if err := s.AuthorizeRegistryOperation(ctx, admin, LocalExecutorID, req.Project, req.Target, req.RegistryID, "registry.example/teamwork/app:v1"); !errors.Is(err, registry.ErrDenied) {
		t.Fatalf("out-of-prefix image was authorized: %v", err)
	}
	operation, err := s.QueueNodeOperation(ctx, admin, LocalExecutorID, 1, req, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegistryOperation(ctx, key, LocalExecutorID, 1, operation.ID, true); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("queued operation received credentials: %v", err)
	}
	claimed, _, err := s.NextNodeOperation(ctx, LocalExecutorID, 1, now)
	if err != nil || claimed.Operation != operation.ID {
		t.Fatalf("claim did not select queued verification: %v", err)
	}
	if _, err := s.RegistryOperation(ctx, key, LocalExecutorID, 2, operation.ID, true); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("wrong generation was authorized: %v", err)
	}
	if err := s.RotateHumanRegistryCredential(ctx, admin, key, item.ID, "robot", "test-replacement"); err != nil {
		t.Fatal(err)
	}
	cred, err := s.RegistryOperation(ctx, key, LocalExecutorID, 1, operation.ID, true)
	if err != nil || cred.Password != "test-replacement" {
		t.Fatalf("current registry credential was not selected: %v", err)
	}
	if err := s.SetHumanRegistryEnabled(ctx, admin, item.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegistryOperation(ctx, key, LocalExecutorID, 1, operation.ID, true); !errors.Is(err, registry.ErrDenied) {
		t.Fatalf("disabled registry delivered credentials to a previously claimed operation: %v", err)
	}
	if err := s.CompleteNodeOperation(ctx, LocalExecutorID, 1, operation.ID, nodeops.Failed, "registry disabled", nil, now); err != nil {
		t.Fatal(err)
	}
	hookBinding := ContainerWebhookBinding{Node: LocalExecutorID, Generation: 1, Container: "app",
		Repository: "registry.example/team/app", Project: "team", Target: "prod", RegistryID: item.ID}
	if _, err := s.CreateContainerWebhook(ctx, admin, hookBinding); !errors.Is(err, registry.ErrDenied) {
		t.Fatalf("disabled registry accepted a webhook binding: %v", err)
	}
	if err := s.SetHumanRegistryEnabled(ctx, admin, item.ID, true); err != nil {
		t.Fatal(err)
	}
	hook, err := s.CreateContainerWebhook(ctx, admin, hookBinding)
	if err != nil {
		t.Fatal(err)
	}
	version := hookBinding.Repository + "@sha256:" + strings.Repeat("a", 64)
	operation, err = s.QueueContainerWebhook(ctx, hook.ID, hook.Token, "version-1", version, now)
	if err != nil {
		t.Fatal(err)
	}
	claimed, _, err = s.NextNodeOperation(ctx, LocalExecutorID, 1, now)
	if err != nil || claimed.Operation != operation.ID {
		t.Fatalf("claim did not select the webhook release: %v", err)
	}
	if _, err := s.RegistryOperation(ctx, key, LocalExecutorID, 1, operation.ID, false); !errors.Is(err, registry.ErrSecureTransport) {
		t.Fatalf("insecure request obtained the webhook registry credential: %v", err)
	}
	cred, err = s.RegistryOperation(ctx, key, LocalExecutorID, 1, operation.ID, true)
	if err != nil || cred.Password != "test-replacement" {
		t.Fatalf("webhook release did not receive the scoped credential: %v", err)
	}
}

func TestUpdateHumanRegistryGuardsCredentialHost(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	admin, err := s.InitializeAdmin(ctx, "admin", "initial password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	anon, err := s.CreateHumanRegistry(ctx, admin, SecretKey{}, RegistryInput{Name: "Anon", Host: "anon.example"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateHumanRegistry(ctx, admin, anon.ID, RegistryInput{Name: "Renamed", Host: "moved.example", RepositoryPrefix: "team"}); err != nil {
		t.Fatal(err)
	}
	var name, host, prefix string
	if err := s.db.QueryRow(`SELECT name,host,repository_prefix FROM registries WHERE id=?`, anon.ID).Scan(&name, &host, &prefix); err != nil {
		t.Fatal(err)
	}
	if name != "Renamed" || host != "moved.example" || prefix != "team" {
		t.Fatalf("update not applied: %q %q %q", name, host, prefix)
	}

	other, err := s.CreateHumanRegistry(ctx, admin, SecretKey{}, RegistryInput{Name: "Other", Host: "other.example"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateHumanRegistry(ctx, admin, anon.ID, RegistryInput{Name: "Other", Host: "moved.example"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate name: %v", err)
	}
	_ = other

	cred, err := s.CreateHumanRegistry(ctx, admin, SecretKey{7}, RegistryInput{Name: "Cred", Host: "cred.example", Username: "u", Password: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateHumanRegistry(ctx, admin, cred.ID, RegistryInput{Name: "Cred", Host: "moved.example"}); !errors.Is(err, ErrRegistryHostBound) {
		t.Fatalf("credential host change: %v", err)
	}
	if err := s.UpdateHumanRegistry(ctx, admin, cred.ID, RegistryInput{Name: "Cred Renamed", Host: "cred.example", RepositoryPrefix: "lib"}); err != nil {
		t.Fatalf("credential registry rename: %v", err)
	}
	if err := s.UpdateHumanRegistry(ctx, admin, cred.ID, RegistryInput{Name: "", Host: "cred.example"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty name: %v", err)
	}
	if err := s.UpdateHumanRegistry(ctx, admin, cred.ID, RegistryInput{Name: "Cred Renamed", Host: "not a host"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad host: %v", err)
	}

	var audit int
	if err := s.db.QueryRow(`SELECT count(*) FROM project_audit WHERE action='registry.updated'`).Scan(&audit); err != nil {
		t.Fatal(err)
	}
	if audit != 2 {
		t.Fatalf("registry.updated audit rows: %d", audit)
	}
}
