package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/control/ci"
	"github.com/maxtanforge/moori/internal/control/human"
	"github.com/maxtanforge/moori/internal/control/observability"
	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/core/nodeops"
)

// This optional browser regression uses deterministic API failures and states.
// TestConsoleSessionOverHTTPS separately verifies the actual session handlers.
func TestBrowserConsole(t *testing.T) {
	browser := os.Getenv("MOORI_BROWSER_BIN")
	if browser == "" {
		t.Skip("set MOORI_BROWSER_BIN to a Chromium executable; Node.js is also required")
	}
	var loggedIn, cancelled atomic.Bool
	mux := http.NewServeMux()
	Mount(mux)
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/setup":
			w.Write([]byte(`{"required":false}`))
		case "/api/v1/auth/login":
			loggedIn.Store(true)
			w.WriteHeader(http.StatusNoContent)
		case "/api/v1/auth/logout":
			loggedIn.Store(false)
			w.WriteHeader(http.StatusNoContent)
		case "/api/v1/auth/me":
			if !loggedIn.Load() {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Write([]byte(`{"username":"operator"}`))
		case "/api/v1/projects":
			w.Write([]byte(`{"items":[{"id":"alpha","role":"operator"},{"id":"broken","role":"operator"}]}`))
		case "/api/v1/projects/alpha/deployments":
			w.Write([]byte(`{"items":[{"id":"stuck","target":"production","release":"v1","state":"needs_attention"}]}`))
		case "/api/v1/projects/alpha/deployments/stuck":
			w.Write([]byte(`{"id":"stuck","release":"v1","state":"needs_attention","digest":"sha256:test","snapshot":{"target":"production"}}`))
		case "/api/v1/projects/alpha/deployments/stuck/cancel":
			cancelled.Store(true)
			w.Write([]byte(`{"id":"stuck"}`))
		case "/api/v1/projects/alpha/targets":
			w.Write([]byte(`{"items":[{"id":"production"}]}`))
		case "/api/v1/projects/alpha/targets/production/releases":
			w.Write([]byte(`{"items":[{"id":"v1"}]}`))
		default:
			http.Error(w, "catalog unavailable", http.StatusServiceUnavailable)
		}
	})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			api.ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "testdata/browser-smoke.mjs", server.URL, browser, t.TempDir())
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("browser: %v\n%s", err, output)
	}
	if !cancelled.Load() {
		t.Fatal("browser did not submit the needs_attention cancellation")
	}
	t.Log(string(output))
}

// Only the test server exposes module bindings used by snapshot and editor
// fixtures. The shipped modules keep their normal module scope.
func serveBrowserFixtures(w http.ResponseWriter, r *http.Request) bool {
	hooks := map[string]string{
		"/app.js":                   "Object.assign(window,{loadNodeResources,refreshVisibleNodeViews}); Object.defineProperty(window,'pendingNodeRead',{get:()=>pendingNodeRead});",
		"/node-workspace.js":        "Object.assign(window,{rememberNodeResourceChange,applyNodeResourceChanges,nodeResourceChanges,renderWorkspaceBody,buildResourceList,clearContainerConfiguration}); Object.defineProperty(window,'nodeSnapshot',{get:()=>nodeSnapshot,set:value=>{nodeSnapshot=value}});",
		"/management.js":            "Object.assign(window,{nodeById,containerRemoveButton,composeRemoveButton,templateDeployment});",
		"/container-maintenance.js": "Object.assign(window,{splitStandaloneImage});",
		"/ci-management.js":         "Object.assign(window,{listCITargets});",
	}
	hook, ok := hooks[r.URL.Path]
	if !ok {
		return false
	}
	body, err := assets.ReadFile(strings.TrimPrefix(r.URL.Path, "/"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return true
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	_, _ = w.Write(body)
	_, _ = fmt.Fprintln(w, "\n"+hook)
	return true
}

func TestBrowserDirectHTTP(t *testing.T) {
	testBrowserSession(t, false)
}

func TestBrowserHTTPSWithRewrittenProxyHost(t *testing.T) {
	testBrowserSession(t, true)
}

func testBrowserSession(t *testing.T, viaProxy bool) {
	t.Helper()
	browser := os.Getenv("MOORI_BROWSER_BIN")
	if browser == "" {
		t.Skip("set MOORI_BROWSER_BIN")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnsureLocalExecutor(ctx); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	Mount(mux)
	human.Mount(mux, store, human.ViewOptions{})
	server := httptest.NewServer(mux)
	defer server.Close()
	address, mode := server.URL, "direct-http"
	if viaProxy {
		upstream, err := url.Parse(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		gateway := httptest.NewTLSServer(&httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(upstream)
			r.Out.Host = upstream.Host
			r.SetXForwarded()
		}})
		defer gateway.Close()
		address, mode = gateway.URL, "proxy-https"
	}
	cmd := exec.CommandContext(ctx, "node", "testdata/browser-smoke.mjs", address, browser, t.TempDir(), mode)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s browser: %v\n%s", mode, err, output)
	}
	t.Log(string(output))
}

func TestBrowserSetup(t *testing.T) {
	browser := os.Getenv("MOORI_BROWSER_BIN")
	if browser == "" {
		t.Skip("set MOORI_BROWSER_BIN")
	}
	// The log check waits for a refresh interval and for refresh to stop.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnsureLocalExecutor(ctx); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	Mount(mux)
	health, err := observability.Open(ctx, filepath.Join(t.TempDir(), "metrics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer health.Close()
	_, gen, err := store.IssueAgentCredential(ctx, "test-worker", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	sampled := time.Now().UTC().Add(-3 * time.Second).Format(time.RFC3339Nano)
	local := fmt.Appendf(nil, `{"available":true,"generated_at":%q,"container_count":2,"running_count":2,"host":{"sampled_at":%q,"hostname":"local-machine","cpu_model":"Test CPU","cpu_percent":24,"memory_total_bytes":1048576,"memory_available_bytes":262144},"engine":{"version":"28.5.2"},"containers":[{"name":"test-web","full_id":%q,"state":"running","image":"test-app:v1","image_id":"sha256:1111111111111111111111111111111111111111111111111111111111111111","compose_project":"test-stack","compose_service":"web","networks":["test-net"],"mounts":[{"type":"volume","name":"test-data","source":"/docker/volumes/test-data/_data","destination":"/data"}],"stats":{"cpu_percent":15.5,"memory_bytes":1048576}},{"name":"edge-proxy","full_id":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","state":"running","image":"nginx:stable","mounts":[{"type":"bind","source":"/srv/site","destination":"/site","read_only":true}]}],"compose_projects":[{"name":"test-stack","services":["web"]}],"network_count":1,"networks":[{"id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","name":"test-net","driver":"bridge"}],"volume_count":1,"volumes":[{"name":"test-data","driver":"local","mountpoint":"/docker/volumes/test-data/_data","ref_count":1}],"image_count":1,"images":[{"full_id":"sha256:1111111111111111111111111111111111111111111111111111111111111111","id":"test-app","tags":["test-app:v1","registry.example.test/team/app:stable"]}]}`, sampled, sampled, strings.Repeat("a", 64))
	remote := []byte(`{"available":true,"image_count":1,"host":{"hostname":"remote-machine"},"images":[{"tags":["test-app:v1"],"size":1048576}]}`)
	if err := health.RecordInventory(ctx, storage.LocalExecutorID, 1, local, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := health.RecordContact(ctx, storage.LocalExecutorID, 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordNodeOperationCapability(ctx, storage.LocalExecutorID, 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := health.RecordInventory(ctx, "test-worker", gen, remote, time.Now().Add(-3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := health.RecordContact(ctx, "test-worker", gen, time.Now().Add(-3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	human.Mount(mux, store, human.ViewOptions{LocalExecution: true, Health: health})
	human.MountRegistries(mux, store, storage.SecretKey{})
	ci.MountContainerDeployments(mux, store)
	pipeline := ci.Handler{Store: store}
	mux.HandleFunc("POST /api/v1/releases", pipeline.Publish)
	mux.Handle("POST /api/v1/deployments", pipeline)
	mux.HandleFunc("POST /_test/enable-ci-node", func(w http.ResponseWriter, r *http.Request) {
		if err := store.RecordNodeOperationCapability(r.Context(), storage.LocalExecutorID, 1, time.Now()); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		if err := store.RecordNodeOperationCapability(r.Context(), "test-worker", gen, time.Now()); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /_test/complete-ci/{id}", func(w http.ResponseWriter, r *http.Request) {
		agent, generation, name := storage.LocalExecutorID, int64(1), "edge-proxy"
		if r.URL.Query().Get("remote") == "1" {
			agent, generation, name = "test-worker", gen, "remote-app"
		}
		req, _, err := store.NextNodeOperation(r.Context(), agent, generation, time.Now())
		if err != nil || req.Operation != r.PathValue("id") || req.Action != nodeops.ContainerRelease || req.Name != name {
			http.Error(w, fmt.Sprintf("unexpected CI fixture claim: %v", err), http.StatusConflict)
			return
		}
		if err := store.CompleteNodeOperation(r.Context(), agent, generation, req.Operation, nodeops.Succeeded, "", &nodeops.Result{Image: req.Image}, time.Now()); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveBrowserFixtures(w, r) {
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/nodes/@local/operations" {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			var req nodeops.Request
			if json.Unmarshal(body, &req) == nil {
				switch req.Action {
				case nodeops.ContainerEditConfig, nodeops.ComposeSourceRead:
					http.Error(w, "fixture has no registered source or container config", http.StatusNotFound)
					return
				}
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		mux.ServeHTTP(w, r)
	}))
	defer server.Close()
	cmd := exec.CommandContext(ctx, "node", "testdata/browser-smoke.mjs", server.URL, browser, t.TempDir(), "setup")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("browser setup: %v\n%s", err, output)
	}
	t.Log(string(output))
}
