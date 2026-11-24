package human

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/control/observability"
	"github.com/maxtanforge/moori/internal/control/storage"
)

func TestMonitoringRoutesAuthorizeAndShowCurrentNodes(t *testing.T) {
	ctx := context.Background()
	metrics, err := observability.Open(ctx, filepath.Join(t.TempDir(), "metrics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer metrics.Close()
	server, store := mountLocalResources(t, ViewOptions{Health: metrics, LocalExecution: true})
	if err := store.EnsureLocalExecutor(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.IssueAgentCredential(ctx, "worker", time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.IssueAgentCredential(ctx, "obsolete", time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := store.RevokeAgentCredential(ctx, "obsolete"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := metrics.RecordContact(ctx, storage.LocalExecutorID, 1, now); err != nil {
		t.Fatal(err)
	}
	observed := now.Add(-5 * time.Second).UTC().Format(time.RFC3339Nano)
	snapshot := []byte(`{"available":true,"generated_at":"` + observed + `","host":{"sampled_at":"` + observed + `","cpu_percent":20,"memory_total_bytes":1024,"memory_available_bytes":256}}`)
	if err := metrics.RecordInventory(ctx, storage.LocalExecutorID, 1, snapshot, now); err != nil {
		t.Fatal(err)
	}
	admin := adminSession(t, server)
	if _, err := store.CreateHumanUser(ctx, "member", "member password"); err != nil {
		t.Fatal(err)
	}
	token, err := store.LoginHuman(ctx, "member", "member password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("GET", server.URL+path, nil)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		server.Config.Handler.ServeHTTP(w, r)
		return w
	}
	if w := get("/api/v1/monitor/nodes", nil); w.Code != 401 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := get("/api/v1/monitor/nodes/worker/history", &http.Cookie{Name: admin.Name, Value: token}); w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
	w := get("/api/v1/monitor/nodes", admin)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var nodes struct {
		Enabled bool                   `json:"enabled"`
		Items   []observability.Health `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &nodes); err != nil {
		t.Fatal(err)
	}
	if !nodes.Enabled || len(nodes.Items) != 2 || nodes.Items[0].Agent != storage.LocalExecutorID || nodes.Items[0].Contact != "online" || nodes.Items[1].Agent != "worker" || nodes.Items[1].Contact != "never_seen" {
		t.Fatalf("unexpected node overview: %+v", nodes)
	}
	for _, path := range []string{
		"/api/v1/monitor/nodes/worker/history?range=1h",
		"/api/v1/monitor/nodes/worker/alerts",
	} {
		if w := get(path, admin); w.Code != 200 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	w = get("/api/v1/monitor/nodes/@local/history?range=1h", admin)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var trend struct {
		Generation int `json:"generation"`
		Points     []struct {
			CPU    float64 `json:"cpu_percent"`
			Memory uint64  `json:"memory_bytes"`
		} `json:"points"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &trend); err != nil {
		t.Fatal(err)
	}
	if trend.Generation != 1 || len(trend.Points) != 1 || trend.Points[0].CPU != 20 || trend.Points[0].Memory != 768 {
		t.Fatalf("unexpected chart data: %+v", trend)
	}
	if w := get("/api/v1/monitor/nodes/worker/history?range=30d", admin); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := get("/api/v1/monitor/nodes/obsolete/history?range=1h", admin); w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
}
