package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/core/deployment"
	"github.com/maxtanforge/moori/internal/core/release"
)

func TestConsoleTemplateDeploymentIsReusableImmutableAndAdminOnly(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	admin, err := store.InitializeAdmin(ctx, "library-admin", "initial password", time.Hour)
	must(err)
	must(store.RegisterTarget(ctx, "project", "old"))
	must(store.RegisterTarget(ctx, "other-project", "new"))
	_, err = store.CreateHumanUser(ctx, "operator", "operator password")
	must(err)
	must(store.SetProjectMember(ctx, admin, "other-project", "operator", "operator"))
	operator, err := store.LoginHuman(ctx, "operator", "operator password", time.Hour)
	must(err)
	const document = `services: {app: {image: 'nginx@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', ports: ['8000:80']}}`
	template, err := store.SaveConsoleTemplate(ctx, admin, "", ConsoleTemplateInput{
		Name: "Web", Compose: document, Parameters: release.TemplateParameters{
			Ports: []release.TemplatePort{{Service: "app", Port: 0}},
		},
	})
	must(err)
	if len(template.Versions) != 1 || template.Versions[0].Version != 1 {
		t.Fatalf("unexpected versions: %+v", template.Versions)
	}
	if _, err := store.ListConsoleTemplates(ctx, operator); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("operator could access library: %v", err)
	}
	a := Admission{ID: "deployment1", Project: "other-project", Target: "new", Key: "request"}
	overrides := release.TemplateOverrides{Ports: []release.TemplatePort{{Service: "app", Port: 0, Value: "9000"}}}
	if _, err := store.AdmitConsoleTemplate(ctx, operator, template.ID, 1, a, overrides); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("operator could instantiate library template: %v", err)
	}
	id, err := store.AdmitConsoleTemplate(ctx, admin, template.ID, 1, a, overrides)
	must(err)
	a.ID = "deployment2"
	replay, err := store.AdmitConsoleTemplate(ctx, admin, template.ID, 1, a, overrides)
	must(err)
	if replay != id {
		t.Fatal("identical retry created another deployment")
	}
	overrides.Ports[0].Value = "9001"
	if _, err := store.AdmitConsoleTemplate(ctx, admin, template.ID, 1, a, overrides); !errors.Is(err, ErrConflict) {
		t.Fatalf("differing request replay: %v", err)
	}
	var count int
	must(store.db.QueryRow("SELECT count(*) FROM target_releases").Scan(&count))
	if count != 1 {
		t.Fatal("conflicting replay published a release")
	}
	view, err := store.Get(ctx, id)
	must(err)
	a.Release = view.Release
	a.Key = "operator-request"
	if _, err := store.AdmitHumanRelease(ctx, operator, a); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("operator deployed a restricted generated release: %v", err)
	}
	doc, err := store.ExecutionSnapshot(ctx, id)
	must(err)
	if !strings.Contains(doc, "9000:80") {
		t.Fatal("snapshot lost bound port")
	}
	revised, err := store.SaveConsoleTemplate(ctx, admin, template.ID, ConsoleTemplateInput{
		Source: &ConsoleTemplateSource{Project: a.Project, Deployment: id},
	})
	must(err)
	if len(revised.Versions) != 2 || revised.Versions[0].Version != 2 || revised.Versions[1].Digest != template.Versions[0].Digest {
		t.Fatalf("saving a new version changed the first: %+v", revised.Versions)
	}
}

func TestConsoleTemplateSnapshotCarriesPortableSecretBindings(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	admin, err := store.InitializeAdmin(ctx, "library-admin", "initial password", time.Hour)
	must(err)
	must(store.RegisterTarget(ctx, "app", "source"))
	must(store.RegisterTarget(ctx, "app", "destination"))
	configure := func(target, version string) {
		must(store.PutSecret(ctx, SecretKey{1}, "app", target, "PASSWORD", version, []byte("private value")))
		must(store.SetTargetSecrets(ctx, "app", target, version, SecretBindings{
			Secrets: map[string]string{"PASSWORD": version}, Services: map[string][]string{"app": {"PASSWORD"}},
		}))
	}
	configure("source", "source-only-version")
	const document = `services: {app: {image: 'nginx@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'}}`
	r, err := release.Compile(ctx, []byte(document), nil, nil, nil)
	must(err)
	must(store.PublishRelease(ctx, "app", "existing", r))
	id, err := store.AdmitHumanRelease(ctx, admin, Admission{ID: "original", Project: "app", Target: "source", Key: "original", Release: "existing"})
	must(err)
	template, err := store.SaveConsoleTemplate(ctx, admin, "", ConsoleTemplateInput{Name: "Portable", Source: &ConsoleTemplateSource{Project: "app", Deployment: id}})
	must(err)
	var stored string
	must(store.db.QueryRow("SELECT compose || secret_services FROM console_template_versions WHERE template_id=?", template.ID).Scan(&stored))
	if strings.Contains(stored, "source-only-version") || strings.Contains(stored, "private value") || !strings.Contains(stored, "PASSWORD") {
		t.Fatal("stored template did not preserve only portable references")
	}
	a := Admission{ID: "reused", Project: "app", Target: "destination", Key: "reuse"}
	if _, err := store.AdmitConsoleTemplate(ctx, admin, template.ID, 1, a, release.TemplateOverrides{}); !errors.Is(err, ErrSecretUnavailable) {
		t.Fatalf("missing destination secret binding accepted: %v", err)
	}
	configure("destination", "destination-version")
	reused, err := store.AdmitConsoleTemplate(ctx, admin, template.ID, 1, a, release.TemplateOverrides{})
	must(err)
	snapshot, err := store.ExecutionSnapshot(ctx, reused)
	must(err)
	if !strings.Contains(snapshot, "destination-version") || strings.Contains(snapshot, "source-only-version") || strings.Contains(snapshot, "private value") {
		t.Fatal("deployment reused source credentials")
	}
}

func TestExternalSnapshotWithoutNodeCannotBeDispatched(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(store.BindAgent(ctx, "replacement", 1, [32]byte{1}))
	must(store.RegisterAssignedTarget(ctx, "project", "target", "replacement", 1))
	artifact, err := release.Compile(ctx, []byte(`services: {app: {image: 'nginx@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', volumes: ['data:/data']}}
volumes: {data: {name: old-data, external: true}}`), nil, nil, nil)
	must(err)
	snapshot, err := artifact.Bind(release.Binding{Target: "target", ConfigVersion: "legacy", HealthTimeout: time.Minute})
	must(err)
	_, err = store.db.Exec("INSERT INTO deployments(id,project,caller,request_key,target,release_id,digest,state) VALUES ('queued','project','former-admin','key','target','console-template-old',?,'queued')", snapshot.Digest())
	must(err)
	_, err = store.db.Exec("INSERT INTO snapshots(deployment_id,document) VALUES ('queued',?)", snapshot.JSON())
	must(err)
	if err := store.Claim(ctx, "queued", "wrong-node", "replacement", 1, time.Now().Add(time.Minute)); !errors.Is(err, ErrConflict) {
		t.Fatalf("direct claim accepted unknown intended node: %v", err)
	}
	if _, err := store.NextOffer(ctx, "replacement", 1, time.Now(), time.Minute); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("scheduler offered unbound external resources: %v", err)
	}
	view, err := store.Get(ctx, "queued")
	must(err)
	if view.State != deployment.Failed {
		t.Fatalf("queued request with unknown node did not fail: %+v", view)
	}
}
