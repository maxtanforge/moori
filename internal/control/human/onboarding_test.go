package human

import (
	"context"
	"github.com/maxtanforge/moori/internal/control/storage"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOnboardingHTTPPermissionsAndLocalCapability(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	token, err := s.InitializeAdmin(ctx, "admin", "initial password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureLocalExecutor(ctx); err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: cookieName, Value: token}
	mux := http.NewServeMux()
	Mount(mux, s, ViewOptions{LocalExecution: true})
	call := func(handler http.Handler, method, path, origin string, session *http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "https://control"+path, strings.NewReader(`{"project":"app","target":"production","local":true}`))
		req.Header.Set("Origin", origin)
		req.Header.Set("Content-Type", "application/json")
		if session != nil {
			req.AddCookie(session)
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		return res
	}
	if w := call(mux, "POST", "/api/v1/targets", "https://other", cookie); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := call(mux, "POST", "/api/v1/targets", "https://control", nil); w.Code != 401 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call(mux, "POST", "/api/v1/targets", "https://control", cookie); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call(mux, "GET", "/api/v1/auth/me", "", cookie); w.Code != 200 || !strings.Contains(w.Body.String(), `"local_execution":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	disabled := http.NewServeMux()
	Mount(disabled, s, ViewOptions{})
	if w := call(disabled, "POST", "/api/v1/targets", "https://control", cookie); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err := s.CreateHumanUser(ctx, "viewer", "viewer password"); err != nil {
		t.Fatal(err)
	}
	viewer, err := s.LoginHuman(ctx, "viewer", "viewer password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []struct{ method, path string }{{"POST", "/api/v1/targets"}, {"GET", "/api/v1/agents"}} {
		if w := call(mux, p.method, p.path, "https://control", &http.Cookie{Name: cookieName, Value: viewer}); w.Code != 403 {
			t.Fatal(p, w.Code, w.Body.String())
		}
	}
}
