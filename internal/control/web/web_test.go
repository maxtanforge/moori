package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/control/human"
	"github.com/maxtanforge/moori/internal/control/storage"
)

func TestEmbeddedConsoleRoutesAndContentPolicy(t *testing.T) {
	mux := http.NewServeMux()
	Mount(mux)
	for _, path := range []string{"/", "/app.js", "/api.js", "/ui-components.js", "/management.js", "/terminal.js", "/user-management.js", "/webhook-management.js", "/platform-audit.js", "/image-management.js", "/node-workspace.js", "/maintenance-page.js", "/compose-maintenance.js", "/container-maintenance.js", "/style.css", "/vendor/xterm.js", "/vendor/xterm.css", "/vendor/addon-fit.js", "/vendor/uplot.js", "/vendor/uplot.css"} {
		r := httptest.NewRequest("GET", "https://control"+path, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	page := httptest.NewRecorder()
	mux.ServeHTTP(page, httptest.NewRequest("GET", "https://control/", nil))
	for _, reference := range []string{`<script type="module" src="/app.js">`, `<script src="/vendor/xterm.js" defer>`, `<script src="/vendor/addon-fit.js" defer>`, `<link rel="stylesheet" href="/vendor/xterm.css">`, `<script src="/vendor/uplot.js" defer>`, `<link rel="stylesheet" href="/vendor/uplot.css">`, `id="container-terminal-output"`} {
		if !strings.Contains(page.Body.String(), reference) {
			t.Errorf("console page does not reference %s", reference)
		}
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "https://control/api/missing", nil))
	if w.Code != 404 {
		t.Fatalf("unknown API returned %d", w.Code)
	}
}

func TestConsoleHistoryRoutesServeDeepLinks(t *testing.T) {
	mux := http.NewServeMux()
	Mount(mux)
	serve := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "https://control"+path, nil))
		return w
	}
	for _, path := range []string{"/node/%40local/containers", "/node/agent.example.com", "/node/web.js", "/deployment/demo/x/edit", "/deployment/web.js/x/edit", "/settings/registries", "/console", "/unknown"} {
		page := serve(path)
		if page.Code != 200 || !strings.Contains(page.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("deep link %s: %d %s", path, page.Code, page.Header().Get("Content-Type"))
		}
		if !strings.Contains(page.Body.String(), `id="console"`) {
			t.Fatalf("deep link %s did not serve the console page", path)
		}
	}
	for _, path := range []string{"/missing.js", "/missing.css", "/api/missing", "/agent/missing", "/webhooks/gitea/missing"} {
		if code := serve(path).Code; code != 404 {
			t.Fatalf("%s returned %d, want 404", path, code)
		}
	}
}

func TestConsoleFavicon(t *testing.T) {
	mux := http.NewServeMux()
	Mount(mux)

	page := httptest.NewRecorder()
	mux.ServeHTTP(page, httptest.NewRequest("GET", "https://control/", nil))
	if !strings.Contains(page.Body.String(), `<link rel="icon" href="/favicon.ico" type="image/x-icon">`) {
		t.Error("console does not link to its favicon")
	}

	icon := httptest.NewRecorder()
	mux.ServeHTTP(icon, httptest.NewRequest("GET", "https://control/favicon.ico", nil))
	if icon.Code != http.StatusOK || icon.Header().Get("Content-Type") != "image/x-icon" {
		t.Fatalf("favicon response: status %d, content type %q", icon.Code, icon.Header().Get("Content-Type"))
	}
	if !bytes.HasPrefix(icon.Body.Bytes(), []byte{0, 0, 1, 0}) {
		t.Fatal("favicon is not an ICO image")
	}
}

// The console is an ES-module graph rooted at app.js. Every named import must
// resolve to a top-level export of the target module, or the browser fails at
// link time before any script runs. This keeps the hand-maintained graph honest.
func TestConsoleModulesResolveImports(t *testing.T) {
	var (
		exports = map[string]map[string]bool{} // module -> exported names
		imports = map[string][]struct {
			from string
			name string
		}{}
	)
	read := func(name string) string {
		data, err := assets.ReadFile(name)
		if err != nil {
			t.Fatalf("embedded asset %s: %v", name, err)
		}
		return string(data)
	}
	modules := []string{"app.js", "api.js", "ui-components.js", "management.js", "terminal.js", "user-management.js", "webhook-management.js", "alert-settings.js", "ci-management.js", "platform-audit.js", "image-management.js", "monitoring.js", "node-workspace.js", "maintenance-page.js", "compose-maintenance.js", "container-maintenance.js"}
	for _, module := range modules {
		exports[module] = map[string]bool{}
		source := read(module)
		for _, line := range strings.Split(source, "\n") {
			if rest, ok := strings.CutPrefix(line, "export "); ok {
				if m := declName(rest); m != "" {
					exports[module][m] = true
				}
			}
			rest, ok := strings.CutPrefix(line, "import ")
			if !ok {
				continue
			}
			open := strings.Index(rest, "{")
			close := strings.Index(rest, "}")
			var from string
			if open >= 0 && close > open {
				from = strings.TrimSpace(rest[close+1:])
			} else {
				from = strings.TrimSpace(rest) // side-effect import: import './x.js';
			}
			from = strings.TrimPrefix(from, "from ")
			from = strings.Trim(from, "';")
			if !strings.HasPrefix(from, "./") {
				continue
			}
			from = strings.TrimPrefix(from, "./")
			if open < 0 || close <= open {
				imports[module] = append(imports[module], struct {
					from string
					name string
				}{from, ""})
				continue
			}
			for _, name := range strings.Split(rest[open+1:close], ",") {
				name = strings.TrimSpace(name)
				if name == "" {
					continue
				}
				imports[module] = append(imports[module], struct {
					from string
					name string
				}{from, name})
			}
		}
	}
	for module, list := range imports {
		for _, edge := range list {
			target, ok := exports[edge.from]
			if !ok {
				t.Errorf("%s imports from unknown module %s", module, edge.from)
				continue
			}
			if edge.name != "" && !target[edge.name] {
				t.Errorf("%s imports %s, which %s does not export", module, edge.name, edge.from)
			}
		}
	}
	// Every module except the entry must be reachable so its top-level wiring
	// (event listeners and boot-time state) executes in the browser.
	reachable := map[string]bool{"app.js": true}
	queue := []string{"app.js"}
	for len(queue) > 0 {
		module := queue[0]
		queue = queue[1:]
		for _, edge := range imports[module] {
			if !reachable[edge.from] {
				reachable[edge.from] = true
				queue = append(queue, edge.from)
			}
		}
	}
	for _, module := range modules {
		if !reachable[module] {
			t.Errorf("%s is not imported by any reachable module; its wiring never executes", module)
		}
	}
}

// The behavioural console smoke in testdata/frontend-smoke boots the real
// page in jsdom and drives history routing end-to-end. It needs npm dev
// dependencies, so environments that install them get the coverage for free
// while `go test ./...` stays hermetic everywhere else.
func TestConsoleFrontendSmoke(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is required for the console smoke")
	}
	dir := filepath.Join("testdata", "frontend-smoke")
	if _, err := os.Stat(filepath.Join(dir, "node_modules", "jsdom")); err != nil {
		t.Skip("console smoke dependencies missing; run `npm install` in testdata/frontend-smoke")
	}
	cmd := exec.Command(node, "smoke.mjs")
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("console smoke: %v\n%s", err, output)
	}
}

func declName(line string) string {
	for _, prefix := range []string{"async function ", "function ", "const ", "let ", "var ", "class "} {
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			fields := strings.FieldsFunc(rest[:min(len(rest), 64)], func(r rune) bool { return r == '(' || r == ' ' || r == '=' || r == ':' || r == ';' || r == ',' })
			if len(fields) > 0 && len(fields[0]) > 0 {
				return fields[0]
			}
		}
	}
	return ""
}

func TestConsoleSessionOverHTTPS(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	admin, err := s.InitializeAdmin(ctx, "admin", "initial admin test password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateHumanUser(ctx, "operator", "initial test password"); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterTarget(ctx, "team", "production"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetProjectMember(ctx, admin, "team", "operator", "viewer"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	Mount(mux)
	human.Mount(mux, s, human.ViewOptions{})
	server := httptest.NewTLSServer(mux)
	defer server.Close()
	client := server.Client()
	client.Jar, err = cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	invoke := func(method, path, body string, want int) *http.Response {
		t.Helper()
		r, err := http.NewRequest(method, server.URL+path, bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Origin", server.URL)
		r.Header.Set("Content-Type", "application/json")
		res, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { res.Body.Close() })
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d", method, path, res.StatusCode)
		}
		return res
	}
	invoke("GET", "/", "", 200)
	invoke("GET", "/api/v1/auth/me", "", 401)
	invoke("POST", "/api/v1/auth/login", `{"username":"operator","password":"initial test password"}`, 204)
	invoke("GET", "/api/v1/auth/me", "", 200)
	res := invoke("GET", "/api/v1/projects", "", 200)
	var page storage.CatalogPage[storage.ProjectSummary]
	if err := json.NewDecoder(res.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "team" {
		t.Fatalf("catalog: %+v", page)
	}
	invoke("POST", "/api/v1/auth/logout", `{}`, 204)
	invoke("GET", "/api/v1/projects", "", 401)
}
