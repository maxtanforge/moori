package ci

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/core/nodeops"
	"github.com/maxtanforge/moori/internal/core/release"
)

func TestScopedContainerDeploymentWithoutWebhook(t *testing.T) {
	ctx := t.Context()
	s, err := storage.Open(ctx, filepath.Join(t.TempDir(), "ci.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	must(s.EnsureLocalExecutor(ctx))
	must(s.RecordNodeOperationCapability(ctx, storage.LocalExecutorID, 1, now))
	const repository = "registry.example/team/app"
	must(s.RegisterContainerTarget(ctx, "team", "production", storage.LocalExecutorID, 1, "app", repository, ""))
	must(s.RegisterAssignedTarget(ctx, "team", "other", storage.LocalExecutorID, 1))
	token, err := s.IssueServiceCredential(ctx, "ci", "team", "production", now.Add(time.Hour))
	must(err)
	publisher, err := s.IssuePublishingCredential(ctx, "publisher", "team", "production", now.Add(time.Hour))
	must(err)
	const compose = `services: {app: {image: 'registry.example/team/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'}}`
	artifact, err := release.Compile(ctx, []byte(compose), nil, nil, nil)
	must(err)
	must(s.PublishRelease(ctx, "team", "release", artifact))
	if _, err = s.PublishForService(ctx, publisher, storage.Publication{Project: "team", Target: "production", ID: "forbidden", Compose: compose}); !errors.Is(err, storage.ErrForbidden) {
		t.Fatalf("container-scoped credential can publish a Compose release: %v", err)
	}
	if _, err = s.AdmitPublishedRelease(ctx, token, storage.Admission{ID: "forbidden", Project: "team", Target: "production", Release: "release", Key: "forbidden"}); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("container target can admit Compose: %v", err)
	}
	other, err := s.IssueServiceCredential(ctx, "other", "team", "other", now.Add(time.Hour))
	must(err)
	sameTarget, err := s.IssueServiceCredential(ctx, "observer", "team", "production", now.Add(time.Hour))
	must(err)
	mux := http.NewServeMux()
	MountContainerDeployments(mux, s)
	const path = "/api/v1/container-deployments"
	call := func(method, path, token, key, body string, expected int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Idempotency-Key", key)
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		mux.ServeHTTP(res, req)
		if res.Code != expected {
			t.Fatalf("%s %s code %d, want %d: %s", method, path, res.Code, expected, res.Body)
		}
		return res
	}
	version := repository + "@sha256:" + strings.Repeat("a", 64)
	v2 := repository + "@sha256:" + strings.Repeat("b", 64)
	body := func(image string) string { return `{"project":"team","target":"production","image":"` + image + `"}` }
	call("POST", path, other, "run-1", body(version), 401)
	call("POST", path, token, "run-1", body(repository+":latest"), 400)
	call("POST", path, token, "run-1", body(strings.Replace(version, "/app@", "/elsewhere@", 1)), 403)
	var op, replay nodeops.Operation
	must(json.Unmarshal(call("POST", path, token, "run-1", body(version), 202).Body.Bytes(), &op))
	must(json.Unmarshal(call("POST", path, token, "run-1", body(version), 202).Body.Bytes(), &replay))
	if op.ID == "" || replay.ID != op.ID {
		t.Fatalf("inconsistent operation: %+v %+v", op, replay)
	}
	call("POST", path, token, "run-1", body(v2), 409)
	call("GET", path+"/"+op.ID, sameTarget, "", "", 404)
	req, _, err := s.NextNodeOperation(ctx, storage.LocalExecutorID, 1, time.Now())
	must(err)
	if req.Operation != op.ID || req.Action != nodeops.ContainerRelease || req.Name != "app" || req.Image != version {
		t.Fatalf("bad dispatch: %+v", req)
	}
	_, err = s.RunningNodeOperation(ctx, storage.LocalExecutorID, 1, op.ID, time.Now())
	must(err)
	must(s.CompleteNodeOperation(ctx, storage.LocalExecutorID, 1, op.ID, nodeops.Succeeded, "", &nodeops.Result{Image: version}, time.Now()))
	res := call("GET", path+"/"+op.ID, token, "", "", 200)
	if !strings.Contains(res.Body.String(), `"state":"succeeded"`) {
		t.Fatal(res.Body.String())
	}
	call("POST", path, token, "run-2", body(v2), 202)
	must(s.RevokeServiceCredential(ctx, "ci"))
	call("GET", path+"/"+op.ID, token, "", "", 401)
	if _, _, err = s.NextNodeOperation(ctx, storage.LocalExecutorID, 1, time.Now()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("revoked task could still dispatch: %v", err)
	}
}

func TestOneCITokenDeploysLocalAndRemoteTargets(t *testing.T) {
	ctx := t.Context()
	s, err := storage.Open(ctx, filepath.Join(t.TempDir(), "multi.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	admin, err := s.InitializeAdmin(ctx, "admin", "test admin password", time.Hour)
	must(err)
	must(s.EnsureLocalExecutor(ctx))
	_, remoteGeneration, err := s.IssueAgentCredential(ctx, "remote", time.Hour)
	must(err)
	must(s.RecordNodeOperationCapability(ctx, storage.LocalExecutorID, 1, now))
	must(s.RecordNodeOperationCapability(ctx, "remote", remoteGeneration, now))
	const repo = "registry.example/team/app"
	must(s.RegisterContainerTarget(ctx, "local-project", "local", storage.LocalExecutorID, 1, "app", repo, ""))
	must(s.RegisterContainerTarget(ctx, "remote-project", "remote", "remote", remoteGeneration, "app", repo, ""))
	must(s.RegisterAssignedTarget(ctx, "release-project", "release", "remote", remoteGeneration))
	must(s.RegisterAssignedTarget(ctx, "release-project", "compose-secondary", "remote", remoteGeneration))
	must(s.RegisterAssignedTarget(ctx, "release-project", "read-only", storage.LocalExecutorID, 1))
	must(s.RegisterContainerTarget(ctx, "other", "ungranted", storage.LocalExecutorID, 1, "elsewhere", repo, ""))
	if _, err = s.IssueCIPipelineToken(ctx, admin, "local", "invalid", 24, false, storage.CIPipelineGrant{Project: "remote-project", Target: "remote", AllowPublish: true}); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("container publish grant accepted: %v", err)
	}
	token, err := s.IssueCIPipelineToken(ctx, admin, "local", "gitea", 24, false,
		storage.CIPipelineGrant{Project: "remote-project", Target: "remote"},
		storage.CIPipelineGrant{Project: "release-project", Target: "release", AllowPublish: true},
		storage.CIPipelineGrant{Project: "release-project", Target: "compose-secondary", AllowPublish: true},
		storage.CIPipelineGrant{Project: "release-project", Target: "read-only"})
	must(err)
	grants, err := s.ListCIPipelineTokens(ctx, admin, "remote")
	must(err)
	if len(grants) != 1 || len(grants[0].Grants) != 5 || grants[0].ID != "gitea" {
		t.Fatalf("grants not visible from remote target: %+v", grants)
	}
	image := repo + "@sha256:" + strings.Repeat("b", 64)
	local, err := s.QueueCIContainerDeployment(ctx, token, "local-project", "local", "same-run", image, now)
	must(err)
	remote, err := s.QueueCIContainerDeployment(ctx, token, "remote-project", "remote", "same-run", image, now)
	must(err)
	if local.ID == remote.ID || local.Agent != storage.LocalExecutorID || remote.Agent != "remote" {
		t.Fatalf("bad placements: %+v %+v", local, remote)
	}
	if _, err = s.QueueCIContainerDeployment(ctx, token, "other", "ungranted", "same-run", image, now); !errors.Is(err, storage.ErrForbidden) {
		t.Fatalf("ungranted target accepted: %v", err)
	}
	for _, job := range []nodeops.Operation{local, remote} {
		req, _, err := s.NextNodeOperation(ctx, job.Agent, job.Generation, time.Now())
		must(err)
		if req.Operation != job.ID || req.Image != image {
			t.Fatalf("bad node claim: %+v", req)
		}
		must(s.CompleteNodeOperation(ctx, job.Agent, job.Generation, job.ID, nodeops.Succeeded, "", &nodeops.Result{Image: image}, time.Now()))
		result, err := s.ReadCIContainerDeployment(ctx, token, job.ID, time.Now())
		must(err)
		if result.State != nodeops.Succeeded {
			t.Fatalf("cannot read scoped operation: %+v", result)
		}
	}
	const compose = `services: {app: {image: 'registry.example/team/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'}}`
	_, err = s.PublishForService(ctx, token, storage.Publication{Project: "release-project", Target: "release", ID: "v1", Compose: compose})
	must(err)
	_, err = s.PublishForService(ctx, token, storage.Publication{Project: "release-project", Target: "compose-secondary", ID: "v1", Compose: compose})
	must(err)
	_, err = s.AdmitPublishedRelease(ctx, token, storage.Admission{ID: "first", Project: "release-project", Target: "release", Release: "v1", Key: "same-run"})
	must(err)
	_, err = s.AdmitPublishedRelease(ctx, token, storage.Admission{ID: "second", Project: "release-project", Target: "compose-secondary", Release: "v1", Key: "same-run"})
	must(err)
	if _, err = s.PublishForService(ctx, token, storage.Publication{Project: "release-project", Target: "read-only", ID: "v1", Compose: compose}); !errors.Is(err, storage.ErrForbidden) {
		t.Fatalf("deployment-only grant allowed publication: %v", err)
	}
	_, err = s.QueueCIContainerDeployment(ctx, token, "remote-project", "remote", "revoke-me", image, time.Now())
	must(err)
	must(s.RevokeCIPipelineToken(ctx, admin, "remote", "gitea"))
	if _, err = s.ReadCIContainerDeployment(ctx, token, local.ID, time.Now()); !errors.Is(err, storage.ErrForbidden) {
		t.Fatalf("revocation did not invalidate local scope: %v", err)
	}
	if _, _, err = s.NextNodeOperation(ctx, "remote", remoteGeneration, time.Now()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("revoked remote operation dispatched: %v", err)
	}
}
