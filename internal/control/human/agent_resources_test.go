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
	get := func(agent string, session *http.Cookie, validators ...string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", server.URL+"/api/v1/nodes/"+agent+"/resources", nil)
		for _, tag := range validators {
			r.Header.Add("If-None-Match", tag)
		}
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
	tag := w.Header().Get("ETag")
	if tag == "" {
		t.Fatal("resource view has no ETag")
	}
	if w := get("agent-9", cookie, tag); w.Code != 304 || w.Body.Len() != 0 || w.Header().Get("ETag") != tag {
		t.Fatal("unchanged inventory", w.Code, w.Body.String())
	}
	if err := health.RecordContact(ctx, "agent-9", 2, time.Now()); err != nil {
		t.Fatal(err)
	}
	w = get("agent-9", cookie, tag)
	if w.Code != 200 || w.Header().Get("ETag") == tag || !strings.Contains(w.Body.String(), "online") {
		t.Fatal("contact state changed", w.Code, w.Body.String())
	}
	tag = w.Header().Get("ETag")
	contact := time.Now().Add(11 * time.Second).Truncate(time.Millisecond).UTC()
	if err := health.RecordContact(ctx, "agent-9", 2, contact); err != nil {
		t.Fatal(err)
	}
	if w := get("agent-9", cookie, tag); w.Code != 304 || w.Header().Get("X-Moori-Last-Contact") != contact.Format(time.RFC3339Nano) {
		t.Fatal("heartbeat metadata", w.Code, w.Header())
	}
	// Two different inventories can be accepted within the same millisecond.
	at := time.Now()
	updated := localinv.Overview{Available: true, GeneratedAt: at, SectionTimes: map[string]time.Time{"containers": at.Add(-3 * time.Minute)}, ContainerCount: 4}
	for _, count := range []int{4, 5} {
		updated.ContainerCount = count
		document, err := json.Marshal(updated)
		if err != nil {
			t.Fatal(err)
		}
		if err := health.RecordInventory(ctx, "agent-9", 2, document, at); err != nil {
			t.Fatal(err)
		}
		w = get("agent-9", cookie, tag)
		if w.Code != 200 || w.Header().Get("ETag") == tag || !strings.Contains(w.Body.String(), `"stale":true`) {
			t.Fatal("changed inventory with old Docker sample", w.Code, w.Body.String())
		}
		tag = w.Header().Get("ETag")
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
	if w := get("agent-9", cookie, tag); w.Code != 200 || strings.Contains(w.Body.String(), `"overview"`) || !strings.Contains(w.Body.String(), `"contact":"never_seen"`) {
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
	member.Header.Set("If-None-Match", tag)
	member.AddCookie(lw.Result().Cookies()[0])
	mw := httptest.NewRecorder()
	memberServer.Config.Handler.ServeHTTP(mw, member)
	if mw.Code != http.StatusForbidden {
		t.Fatal("member access must be forbidden:", mw.Code)
	}
	anonymous := httptest.NewRequest("GET", memberServer.URL+"/api/v1/nodes/agent-9/resources", nil)
	anonymous.Header.Set("If-None-Match", tag)
	aw := httptest.NewRecorder()
	memberServer.Config.Handler.ServeHTTP(aw, anonymous)
	if aw.Code != http.StatusUnauthorized {
		t.Fatal("anonymous access must be unauthorized:", aw.Code)
	}
}
