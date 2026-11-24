package human

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/control/observability"
	"github.com/maxtanforge/moori/internal/control/storage"
)

func TestNodeDisplayNamesPersistWithoutChangingIdentity(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "control.db")
	s, err := storage.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	health, err := observability.Open(ctx, filepath.Join(dir, "metrics.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = health.Close() })
	admin, err := s.InitializeAdmin(ctx, "admin", "initial password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateHumanUser(ctx, "member", "member password"); err != nil {
		t.Fatal(err)
	}
	member, err := s.LoginHuman(ctx, "member", "member password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureLocalExecutor(ctx); err != nil {
		t.Fatal(err)
	}
	_, generation, err := s.IssueAgentCredential(ctx, "edge-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	inventory := []byte("{\"available\":true,\"host\":{\"hostname\":\"local-machine\"}}")
	if err := health.RecordInventory(ctx, storage.LocalExecutorID, 1, inventory, time.Now()); err != nil {
		t.Fatal(err)
	}
	mount := func() *http.ServeMux {
		mux := http.NewServeMux()
		Mount(mux, s, ViewOptions{LocalExecution: true, Health: health})
		return mux
	}
	mux := mount()
	call := func(method, path, payload, session, origin string, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "https://control"+path, strings.NewReader(payload))
		if origin != "" {
			r.Header.Set("Origin", origin)
			r.Header.Set("Content-Type", "application/json")
		}
		if session != "" {
			r.AddCookie(&http.Cookie{Name: cookieName, Value: session})
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: got %d, want %d: %s", method, path, w.Code, status, w.Body.String())
		}
		return w
	}
	update := func(agent, name, session, origin string, status int) {
		t.Helper()
		data, err := json.Marshal(map[string]string{"display_name": name})
		if err != nil {
			t.Fatal(err)
		}
		call("POST", "/api/v1/nodes/"+agent+"/display-name", string(data), session, origin, status)
	}

	const origin = "https://control"
	update("@local", "办公室 Docker", admin, "https://other", http.StatusForbidden)
	update("@local", "办公室 Docker", "", origin, http.StatusUnauthorized)
	update("@local", "办公室 Docker", member, origin, http.StatusForbidden)
	update("@local", "名字\n第二行", admin, origin, http.StatusBadRequest)
	update("@local", strings.Repeat("节", 65), admin, origin, http.StatusBadRequest)
	call("POST", "/api/v1/nodes/@local/display-name", "{}", admin, origin, http.StatusBadRequest)
	update("unknown", "未注册节点", admin, origin, http.StatusNotFound)
	update("@local", "　办公室 Docker　", admin, origin, http.StatusNoContent)
	update("edge-1", strings.Repeat("节", 64), admin, origin, http.StatusNoContent)
	update("edge-1", strings.Repeat("节", 64), admin, origin, http.StatusNoContent)

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = storage.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	mux = mount()

	w := call("GET", "/api/v1/agents", "", admin, "", http.StatusOK)
	var page struct {
		Items []struct {
			ID          string
			Generation  int64
			DisplayName string `json:"display_name"`
		}
		LocalDisplayName string `json:"local_display_name"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "edge-1" || page.Items[0].Generation != generation ||
		page.Items[0].DisplayName != strings.Repeat("节", 64) || page.LocalDisplayName != "办公室 Docker" {
		t.Fatalf("unexpected directory: %s", w.Body.String())
	}
	for _, tc := range []struct {
		agent    string
		name     string
		hostname string
	}{
		{"@local", "办公室 Docker", "local-machine"},
		{"edge-1", strings.Repeat("节", 64), ""},
	} {
		res := call("GET", "/api/v1/nodes/"+tc.agent+"/resources", "", admin, "", http.StatusOK)
		view := struct {
			Agent       string
			DisplayName string `json:"display_name"`
			Overview    struct {
				Host struct{ Hostname string }
			}
		}{}
		if err := json.Unmarshal(res.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		if view.Agent != tc.agent || view.DisplayName != tc.name || view.Overview.Host.Hostname != tc.hostname {
			t.Fatalf("unexpected resource view: %s", res.Body.String())
		}
	}
	update("@local", "　 ", admin, origin, http.StatusNoContent)
	w = call("GET", "/api/v1/nodes/@local/resources", "", admin, "", http.StatusOK)
	if strings.Contains(w.Body.String(), "display_name") {
		t.Fatalf("cleared display name still exposed: %s", w.Body.String())
	}
	if err := s.RevokeAgentCredential(ctx, "edge-1"); err != nil {
		t.Fatal(err)
	}
	update("edge-1", "退役节点", admin, origin, http.StatusNotFound)
	w = call("GET", "/api/v1/audit", "", admin, "", http.StatusOK)
	if strings.Count(w.Body.String(), "node.display_name_changed") != 3 {
		t.Fatalf("expected an audit event per effective change: %s", w.Body.String())
	}
}
