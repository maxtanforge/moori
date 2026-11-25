package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/core/release"
)

func TestCatalogPaginationAndTargetReleaseIsolation(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := s.CreateHumanUser(ctx, "reader", "initial test password")
	must(err)
	must(s.SetPlatformAdmin(ctx, "reader", true))
	token, err := s.LoginHuman(ctx, "reader", "initial test password", time.Hour)
	must(err)
	for i := 0; i < 51; i++ {
		must(s.RegisterTarget(ctx, fmt.Sprintf("project-%02d", i), fmt.Sprintf("target-%02d", i)))
		must(s.RegisterTarget(ctx, "project-00", fmt.Sprintf("extra-%02d", i)))
	}
	projects, err := s.ListHumanProjects(ctx, token, "")
	must(err)
	if len(projects.Items) != 50 || projects.Next != "project-49" || projects.Items[0].Role != "platform_admin" {
		t.Fatalf("first page: %+v", projects)
	}
	projects, err = s.ListHumanProjects(ctx, token, projects.Next)
	must(err)
	if len(projects.Items) != 1 || projects.Items[0].ID != "project-50" || projects.Next != "" {
		t.Fatalf("last page: %+v", projects)
	}
	page, err := s.ListHumanCatalog(ctx, token, "project-00", "targets", "", "")
	must(err)
	if len(page.Items) != 50 || page.Next != "extra-49" {
		t.Fatalf("targets first page: %+v", page)
	}
	page, err = s.ListHumanCatalog(ctx, token, "project-00", "targets", "", page.Next)
	must(err)
	if len(page.Items) != 2 || page.Next != "" {
		t.Fatalf("targets last page: %+v", page)
	}
	const compose = "services: {web: {image: 'nginx@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'}}"
	compiled, err := release.Compile(ctx, []byte(compose), nil, nil, nil)
	must(err)
	credential, err := s.IssuePublishingCredential(ctx, "publisher", "project-00", "target-00", time.Now().Add(time.Hour))
	must(err)
	_, err = s.PublishForService(ctx, credential, Publication{Project: "project-00", Target: "target-00", ID: "scoped", Compose: compose})
	must(err)
	must(s.PublishRelease(ctx, "project-00", "shared", compiled))
	page, err = s.ListHumanCatalog(ctx, token, "project-00", "releases", "target-00", "")
	must(err)
	if len(page.Items) != 2 || page.Items[0].ID != "scoped" || page.Items[1].ID != "shared" {
		t.Fatalf("target releases: %+v", page)
	}
	page, err = s.ListHumanCatalog(ctx, token, "project-00", "releases", "extra-00", "")
	must(err)
	if len(page.Items) != 1 || page.Items[0].ID != "shared" {
		t.Fatalf("scoped release leaked: %+v", page)
	}
	_, err = s.ListHumanCatalog(ctx, token, "project-00", "releases", "target-01", "")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-project target accepted: %v", err)
	}
}
