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
	"github.com/maxtanforge/moori/internal/node/localinv"
)

func TestAgentResourcesForAdmin(t *testing.T) {
	ctx := context.Background()
	health, err := observability.OpenNodeState(ctx, filepath.Join(t.TempDir(), "resources.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { health.Close() })
	overview, err := json.Marshal(localinv.Overview{Available: true, ContainerCount: 4, RunningCount: 3, StoppedCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := health.RecordInventory(ctx, "agent-9", 2, overview, time.Now()); err != nil {
		t.Fatal(err)
	}
	server, store := mountLocalResources(t, ViewOptions{NodeState: health, LocalExecution: true})
	if err := store.BindAgent(ctx, "agent-9", 2, [32]byte{2}); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureLocalExecutor(ctx); err != nil {
		t.Fatal(err)
	}
	if err := health.RecordInventory(ctx, storage.LocalExecutorID, 1, overview, time.Now().Add(-3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := health.RecordContact(ctx, "agent-9", 2, time.Now().Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := health.RecordContact(ctx, storage.LocalExecutorID, 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	cookie := adminSession(t, server)
	get := func(agent string, session *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", server.URL+"/api/v1/nodes/"+agent+"/resources", nil)
		if session != nil {
			r.AddCookie(session)
		}
		w := httptest.NewRecorder()
		server.Config.Handler.ServeHTTP(w, r)
		return w
	}
	w := get("agent-9", cookie)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{`"available":true`, `"agent":"agent-9"`, `"generation":2`, `"container_count":4`, `"running_count":3`, `"contact":"offline"`, `"last_contact":`, `"management_available":false`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s in %s", want, body)
		}
	}
	if w := get(storage.LocalExecutorID, cookie); w.Code != 200 || !strings.Contains(w.Body.String(), `"contact":"online"`) ||
		!strings.Contains(w.Body.String(), `"stale":true`) || !strings.Contains(w.Body.String(), `"container_count":4`) ||
		!strings.Contains(w.Body.String(), `"management_available":false`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if err := store.RecordNodeOperationCapability(ctx, storage.LocalExecutorID, 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	if w := get(storage.LocalExecutorID, cookie); w.Code != 200 || !strings.Contains(w.Body.String(), `"management_available":true`) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String())
	}
	if err := store.BindAgent(ctx, "agent-9", 3, [32]byte{3}); err != nil {
		t.Fatal(err)
	}
	if w := get("agent-9", cookie); w.Code != 200 || strings.Contains(w.Body.String(), `"overview"`) || !strings.Contains(w.Body.String(), `"contact":"never_seen"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if err := health.RecordContact(ctx, "agent-9", 3, time.Now()); err != nil {
		t.Fatal(err)
	}
	if w := get("agent-9", cookie); w.Code != 200 || strings.Contains(w.Body.String(), `"overview"`) || !strings.Contains(w.Body.String(), `"contact":"online"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	_, generation, err := store.IssueAgentCredential(ctx, "revoked-worker", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := health.RecordContact(ctx, "revoked-worker", generation, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := health.RecordInventory(ctx, "revoked-worker", generation, overview, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.RevokeAgentCredential(ctx, "revoked-worker"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"ghost", "revoked-worker"} {
		w := get(id, cookie)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"available":false`) ||
			!strings.Contains(w.Body.String(), `"contact":"disabled"`) || strings.Contains(w.Body.String(), `"overview"`) {
			t.Fatal(id, w.Code, w.Body.String())
		}
	}
	// Non-administrators are rejected like local machine resources.
	memberServer, memberStore := mountLocalResources(t, ViewOptions{Health: health})
	if _, err := memberStore.CreateHumanUser(context.Background(), "operator", "member password"); err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRequest("POST", memberServer.URL+"/api/v1/auth/login", strings.NewReader(`{"username":"operator","password":"member password"}`))
	login.Header.Set("Content-Type", "application/json")
	login.Header.Set("Origin", memberServer.URL)
	lw := httptest.NewRecorder()
	memberServer.Config.Handler.ServeHTTP(lw, login)
	if lw.Code != http.StatusNoContent {
		t.Fatal(lw.Code, lw.Body.String())
	}
	member := httptest.NewRequest("GET", memberServer.URL+"/api/v1/nodes/agent-9/resources", nil)
	member.AddCookie(lw.Result().Cookies()[0])
	mw := httptest.NewRecorder()
	memberServer.Config.Handler.ServeHTTP(mw, member)
	if mw.Code != http.StatusForbidden {
		t.Fatal("member access must be forbidden:", mw.Code)
	}
	anonymous := httptest.NewRequest("GET", memberServer.URL+"/api/v1/nodes/agent-9/resources", nil)
	aw := httptest.NewRecorder()
	memberServer.Config.Handler.ServeHTTP(aw, anonymous)
	if aw.Code != http.StatusUnauthorized {
		t.Fatal("anonymous access must be unauthorized:", aw.Code)
	}
}
