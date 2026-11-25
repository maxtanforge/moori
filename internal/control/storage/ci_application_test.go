package storage

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/core/nodeops"
	"github.com/maxtanforge/moori/internal/core/registry"
	"github.com/maxtanforge/moori/internal/core/release"
)

func TestComposeCIEnvironmentPublicationAndHistory(t *testing.T) {
	ctx := t.Context()
	s := openTestStore(t, filepath.Join(t.TempDir(), "ci.db"))
	admin, err := s.InitializeAdmin(ctx, "admin", "initial password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.EnsureLocalExecutor(ctx); err != nil {
		t.Fatal(err)
	}
	b := CIPipelineTarget{Kind: "compose", Project: "app", Target: "app-production", Node: LocalExecutorID, Generation: 1}
	created, err := s.CreateCIPipelineTarget(ctx, admin, b)
	if err != nil || created.Kind != "compose" {
		t.Fatal(created, err)
	}
	if _, err = s.CreateCIPipelineTarget(ctx, admin, b); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate environment", err)
	}
	b.Target = "invalid"
	b.Container = "must-not-be-accepted"
	if _, err = s.CreateCIPipelineTarget(ctx, admin, b); !errors.Is(err, ErrInvalid) {
		t.Fatal("mixed deployment types", err)
	}
	b.Container = ""
	b.Kind = "unsupported"
	if _, err = s.CreateCIPipelineTarget(ctx, admin, b); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown deployment type", err)
	}
	token, err := s.IssueCIPipelineToken(ctx, admin, created.Target, "pipeline", 1, true)
	if err != nil {
		t.Fatal(err)
	}
	image := "docker.io/library/nginx@sha256:" + strings.Repeat("a", 64)
	p := Publication{Project: "app", Target: created.Target, ID: "ci-1", Compose: "services: {web: {image: nginx:build}}", Images: map[string]string{"web": image}}
	first, err := s.PublishForService(ctx, token, p)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := s.PublishForService(ctx, token, p); err != nil || first != second {
		t.Fatal("publication retry", second, err)
	}
	if _, err = s.AdmitPublishedRelease(ctx, token, Admission{ID: "deploy-1", Project: p.Project, Target: p.Target, Release: p.ID, Key: "ci-1"}); err != nil {
		t.Fatal(err)
	}
	initial, err := s.PageCIPipelineRuns(ctx, admin, p.Target, "", 20)
	runs := initial.Items
	if err != nil || len(runs) != 1 || runs[0].Kind != "compose" || runs[0].Release != p.ID || !strings.HasPrefix(runs[0].Digest, "sha256:") || runs[0].CreatedAt.IsZero() {
		t.Fatal(runs, err)
	}
	old := time.Now().UTC().Add(-48 * time.Hour).Format(time.DateTime)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO deployment_audit(deployment_id,state,reason,created_at)
 VALUES ('deploy-1','failed','node rejected the image',?)`, old); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE deployment_audit SET created_at=? WHERE deployment_id='deploy-1'", old); err != nil {
		t.Fatal(err)
	}
	if err := s.PruneAudit(ctx, time.Now().Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	retained, err := s.PageCIPipelineRuns(ctx, admin, p.Target, "", 20)
	if err != nil || len(retained.Items) != 1 || !retained.Items[0].CreatedAt.Equal(runs[0].CreatedAt) ||
		retained.Items[0].Error != "node rejected the image" {
		t.Fatalf("deployment history was lost with audit rows: %+v, %v", retained, err)
	}
	for _, id := range []string{"z-second", "a-third"} {
		if _, err = s.AdmitPublishedRelease(ctx, token, Admission{ID: id, Project: p.Project, Target: p.Target, Release: p.ID, Key: id}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.PageCIPipelineRuns(ctx, admin, p.Target, "", 2)
	if err != nil || len(page.Items) != 2 || page.Items[0].ID != "a-third" || page.Items[1].ID != "z-second" || page.Next == "" {
		t.Fatalf("first Compose page: %+v, %v", page, err)
	}
	if _, err = s.AdmitPublishedRelease(ctx, token, Admission{ID: "new", Project: p.Project, Target: p.Target, Release: p.ID, Key: "new"}); err != nil {
		t.Fatal(err)
	}
	page, err = s.PageCIPipelineRuns(ctx, admin, p.Target, page.Next, 2)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != "deploy-1" || page.Next != "" {
		t.Fatalf("second Compose page: %+v, %v", page, err)
	}
	scoped, err := s.PageManagedComposeDeploymentRuns(ctx, admin, LocalExecutorID, release.ComposeProjectName(p.Target), "", 2)
	if err != nil || scoped.Target != p.Target || scoped.Project != p.Project || len(scoped.Items) != 2 || scoped.Items[0].ID != "new" {
		t.Fatalf("Compose resource history without a successful deployment: %+v, %v", scoped, err)
	}
	p.ID = "bad-service"
	p.Images = map[string]string{"missing": image}
	if _, err = s.PublishForService(ctx, token, p); !errors.Is(err, ErrInvalid) {
		t.Fatal("nonexistent service override", err)
	}
	p.ID = "unpinned"
	p.Images = map[string]string{"web": "nginx:latest"}
	if _, err = s.PublishForService(ctx, token, p); !errors.Is(err, ErrInvalid) {
		t.Fatal("unpinned override", err)
	}
	p.ID = "other-environment"
	p.Target = "other"
	if _, err = s.PublishForService(ctx, token, p); !errors.Is(err, ErrForbidden) {
		t.Fatal("cross environment publish", err)
	}
}

func TestContainerDeploymentHistorySourcesAndPaging(t *testing.T) {
	ctx := t.Context()
	s := openTestStore(t, filepath.Join(t.TempDir(), "container-history.db"))
	admin, err := s.InitializeAdmin(ctx, "admin", "initial password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.EnsureLocalExecutor(ctx))
	target := CIPipelineTarget{Project: "app", Target: "test-target", Kind: "container",
		Node: LocalExecutorID, Generation: 1, Container: "web", Repository: "docker.io/library/nginx"}
	_, err = s.CreateCIPipelineTarget(ctx, admin, target)
	must(err)
	_, err = s.IssueCIPipelineToken(ctx, admin, target.Target, "build", 1, false)
	must(err)
	now := time.Now()
	created := now.UnixMilli()
	manual, ci, webhook := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32)
	exec := func(query string, args ...any) {
		t.Helper()
		_, err := s.db.ExecContext(ctx, query, args...)
		must(err)
	}
	insert := `INSERT INTO node_operations(id,agent,generation,actor,action,request,mutation,state,created_ms,expires_ms,history_target)
 VALUES (?,'@local',?,'operator',?,'{}',1,?,?,?,?)`
	instance := strings.Repeat("2", 64)
	exec(insert, manual, 1, "container.replace", "running", created, created+60000, strings.Repeat("1", 64))
	exec(insert, ci, 1, "container.release", "running", created, created+60000, "web")
	exec(insert, webhook, 1, "container.release", "succeeded", created, created+60000, "web")
	exec(insert, strings.Repeat("d", 32), 2, "container.release", "succeeded", created, created+60000, "web")
	exec(insert, strings.Repeat("e", 32), 1, "container.restart", "succeeded", created, created+60000, "web")
	exec(`INSERT INTO ci_container_deliveries(credential,delivery_key,target,image,operation)
 VALUES ('build','delivery','test-target','nginx:ci',?)`, ci)
	exec(`INSERT INTO container_webhooks(id,token_hash,actor,agent,generation,name,repository,project,target,registry_id)
 VALUES ('hook',zeroblob(32),'operator','@local',1,'web','nginx','app','test-target','')`)
	exec(`INSERT INTO container_webhook_deliveries(webhook,delivery_key,image,operation) VALUES ('hook','delivery','nginx:hook',?)`, webhook)
	must(s.CompleteNodeOperation(ctx, LocalExecutorID, 1, manual, nodeops.Succeeded, "",
		&nodeops.Result{Container: &nodeops.Container{Name: "web", Image: "nginx:manual"}}, now))
	must(s.CompleteNodeOperation(ctx, LocalExecutorID, 1, ci, nodeops.Succeeded, "",
		&nodeops.Result{Container: &nodeops.Container{Name: "web", FullID: instance, Image: "nginx:ci"}}, now))
	exec("UPDATE node_operations SET result=NULL WHERE id=?", manual)
	first, err := s.PageContainerDeploymentRuns(ctx, admin, LocalExecutorID, 1, "web", "", "", 2)
	must(err)
	if len(first.Items) != 2 || first.Next == "" || first.Items[0].Source != "webhook" || first.Items[1].Source != "ci" {
		t.Fatalf("first page: %+v", first)
	}
	second, err := s.PageContainerDeploymentRuns(ctx, admin, LocalExecutorID, 1, "web", "", first.Next, 2)
	must(err)
	if len(second.Items) != 1 || second.Next != "" || second.Items[0].Source != "manual" || second.Items[0].Image != "nginx:manual" {
		t.Fatalf("second page after container replacement and result pruning: %+v", second)
	}
	// The instance view isolates the exact container ID: only the operation
	// whose result recorded this instance, regardless of name changes, while
	// operations without a recorded result container stay in the name view.
	instancePage, err := s.PageContainerDeploymentRuns(ctx, admin, LocalExecutorID, 1, "web", instance, "", 20)
	must(err)
	if len(instancePage.Items) != 1 || instancePage.Items[0].ID != ci || instancePage.Next != "" {
		t.Fatalf("instance page: %+v", instancePage)
	}
	unknown, err := s.PageContainerDeploymentRuns(ctx, admin, LocalExecutorID, 1, "web", strings.Repeat("9", 64), "", 20)
	must(err)
	if len(unknown.Items) != 0 {
		t.Fatalf("unknown instance returned rows: %+v", unknown)
	}
}

func TestCIContainerTargetSelectsSharedRegistry(t *testing.T) {
	ctx := t.Context()
	s := openTestStore(t, filepath.Join(t.TempDir(), "ci.db"))
	admin, err := s.InitializeAdmin(ctx, "admin", "initial password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureLocalExecutor(ctx); err != nil {
		t.Fatal(err)
	}
	reg, err := s.CreateHumanRegistry(ctx, admin, SecretKey{1}, RegistryInput{
		Name: "shared", Host: "registry.example", RepositoryPrefix: "team", Username: "ci", Password: "test-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	b := CIPipelineTarget{Project: "app", Target: "prod", Node: LocalExecutorID, Generation: 1, Container: "web", Repository: "registry.example/teamwork/web", RegistryID: reg.ID}
	if _, err := s.CreateCIPipelineTarget(ctx, admin, b); !errors.Is(err, registry.ErrDenied) {
		t.Fatalf("selected credential outside its repository: %v", err)
	}
	b.Repository = "registry.example/team/web"
	if _, err := s.CreateCIPipelineTarget(ctx, admin, b); err != nil {
		t.Fatal(err)
	}
}
