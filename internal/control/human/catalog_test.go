package human

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/control/storage"
)

func TestBrowserCatalogUsesCurrentProjectGrants(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, p := range []string{"alpha", "beta"} {
		if err := s.RegisterTarget(ctx, p, p+"-target"); err != nil {
			t.Fatal(err)
		}
	}
	admin, err := s.InitializeAdmin(ctx, "admin", "initial admin test password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateHumanUser(ctx, "member", "initial test password"); err != nil {
		t.Fatal(err)
	}
	token, err := s.LoginHuman(ctx, "member", "initial test password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetProjectMember(ctx, admin, "alpha", "member", "viewer"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	Mount(mux, s, ViewOptions{})
	invoke := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "https://control"+path, nil)
		r.AddCookie(&http.Cookie{Name: cookieName, Value: token})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	w := invoke("/api/v1/projects")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"alpha"`) || strings.Contains(w.Body.String(), `"id":"beta"`) {
		t.Fatalf("project visibility: %d %s", w.Code, w.Body.String())
	}
	w = invoke("/api/v1/projects/alpha/targets")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "alpha-target") {
		t.Fatalf("targets: %d %s", w.Code, w.Body.String())
	}
	for _, path := range []string{"/api/v1/projects/beta/targets", "/api/v1/projects/beta/deployments", "/api/v1/projects/beta/targets/beta-target/releases"} {
		w = invoke(path)
		if w.Code != 403 {
			t.Fatalf("cross-project %s: %d", path, w.Code)
		}
	}
	if err := s.SetProjectMember(ctx, admin, "alpha", "member", "none"); err != nil {
		t.Fatal(err)
	}
	w = invoke("/api/v1/projects/alpha/targets")
	if w.Code != 403 {
		t.Fatalf("revoked grant: %d", w.Code)
	}
}
