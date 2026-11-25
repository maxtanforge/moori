package human

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/control/storage"
)

func TestCreateAgentTokenWithoutDeploymentTarget(t *testing.T) {
	ctx := t.Context()
	s, err := storage.Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	admin, err := s.InitializeAdmin(ctx, "admin", "initial password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateHumanUser(ctx, "viewer", "viewer password"); err != nil {
		t.Fatal(err)
	}
	viewer, err := s.LoginHuman(ctx, "viewer", "viewer password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	Mount(mux, s, ViewOptions{})
	call := func(method, path, session, origin, body string, want int, keys ...string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "https://control"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		if session != "" {
			r.AddCookie(&http.Cookie{Name: cookieName, Value: session})
		}
		if len(keys) != 0 {
			r.Header.Set("Idempotency-Key", keys[0])
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s = %d, want %d", method, path, w.Code, want)
		}
		return w
	}
	const agents = "/api/v1/agents"
	const body = `{"id":"edge-1"}`
	call("POST", agents, "", "https://control", body, 401)
	call("POST", agents, viewer, "https://control", body, 403)
	call("POST", agents, admin, "https://other", body, 403)
	call("POST", agents, admin, "https://control", `{"id":"@local"}`, 400)
	w := call("POST", agents, admin, "https://control", body, 201)
	var issued struct {
		ID         string `json:"id"`
		Generation int64  `json:"generation"`
		Token      string `json:"token"`
	}
	if err := json.NewDecoder(w.Body).Decode(&issued); err != nil || issued.ID != "edge-1" || issued.Generation != 1 ||
		w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("invalid token creation response", err)
	}
	call("POST", agents, admin, "https://control", body, 409)
	if id, generation, err := s.AuthenticateAgentCredential(ctx, issued.Token); err != nil || id != issued.ID || generation != issued.Generation {
		t.Fatal("issued token could not authenticate after a duplicate request", err)
	}
	w = call("GET", agents, admin, "https://control", "", 200)
	if !strings.Contains(w.Body.String(), `"id":"edge-1"`) || strings.Contains(w.Body.String(), issued.Token) {
		t.Fatal("agent directory missing entry or exposed its token")
	}
	page, err := s.ListHumanProjects(ctx, admin, "")
	if err != nil || len(page.Items) != 0 {
		t.Fatal("issuing a token must not create a project", err)
	}
	call("POST", "/api/v1/targets", admin, "https://control", `{"project":"app","target":"prod","agent":"edge-1","generation":1}`, 204)
	const retryBody = `{"id":"edge-2"}`
	key := strings.Repeat("a", 64)
	first := call("POST", agents, admin, "https://control", retryBody, 201, key)
	retry := call("POST", agents, admin, "https://control", retryBody, 201, key)
	if first.Body.String() != retry.Body.String() {
		t.Fatal("retry changed the issued token")
	}
	call("POST", agents, admin, "https://control", retryBody, 409, strings.Repeat("b", 64))
	const precondition = `{"generation":1}`
	call("DELETE", agents+"/edge-2", viewer, "https://control", precondition, 403)
	call("DELETE", agents+"/edge-2", admin, "https://other", precondition, 403)
	call("DELETE", agents+"/@local", admin, "https://control", precondition, 400)
	call("DELETE", agents+"/edge-2", admin, "https://control", `{"generation":2}`, 409)
	call("DELETE", agents+"/edge-2", admin, "https://control", precondition, 204)
	call("DELETE", agents+"/edge-2", admin, "https://control", precondition, 404)
	call("POST", agents, admin, "https://control", retryBody, 409, key)
	call("POST", agents, admin, "https://control", retryBody, 409, strings.ToUpper(key))
	if page := call("GET", agents, admin, "https://control", "", 200); strings.Contains(page.Body.String(), "edge-2") {
		t.Fatal("deleted node appeared in directory")
	}
	replacement := call("POST", agents, admin, "https://control", retryBody, 201, strings.Repeat("b", 64))
	if !strings.Contains(replacement.Body.String(), `"generation":2`) {
		t.Fatal("replacement reused original generation")
	}
	if retry := call("POST", agents, admin, "https://control", retryBody, 201, strings.Repeat("b", 64)); replacement.Body.String() != retry.Body.String() {
		t.Fatal("replacement enrollment retry rotated token")
	}
	call("DELETE", agents+"/edge-2", admin, "https://control", precondition, 409)
	call("DELETE", agents+"/edge-1", admin, "https://control", precondition, 204)
	if _, _, err := s.AuthenticateAgentCredential(ctx, issued.Token); err == nil {
		t.Fatal("deleted Agent token authenticated")
	}
	if _, _, err := s.ReadHumanTargetAgent(ctx, admin, "app", "prod"); err == nil {
		t.Fatal("deleted node retained a target")
	}
}
