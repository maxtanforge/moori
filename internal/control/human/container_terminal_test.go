package human

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/maxtanforge/moori/internal/control/dispatch"
	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/transport/execstream"
)

const terminalContainer = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func terminalServer(t *testing.T, starters ...execstream.StartTerminal) (*httptest.Server, *storage.Store, *http.Cookie, *execstream.Broker, string, int64) {
	t.Helper()
	ctx := t.Context()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	for _, name := range []string{"member", "admin"} {
		if _, err := store.CreateHumanUser(ctx, name, name+" test password"); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetPlatformAdmin(ctx, "admin", true); err != nil {
		t.Fatal(err)
	}
	token, generation, err := store.IssueAgentCredential(ctx, "remote", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	Mount(mux, store, ViewOptions{})
	broker := new(execstream.Broker)
	var starter execstream.StartTerminal
	if len(starters) > 0 {
		starter = starters[0]
		if err := store.EnsureLocalExecutor(ctx); err != nil {
			t.Fatal(err)
		}
	}
	MountContainerTerminal(mux, store, broker, starter)
	mux.Handle("GET /agent/v1/exec-stream", dispatch.ExecStreamEndpoint(store, broker))
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)

	login := httptest.NewRequest("POST", "https://control/api/v1/auth/login", strings.NewReader(`{"username":"admin","password":"admin test password"}`))
	login.Header.Set("Content-Type", "application/json")
	login.Header.Set("Origin", "https://control")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, login)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("admin login: %d %s", rec.Code, rec.Body)
	}
	return server, store, rec.Result().Cookies()[0], broker, token, generation
}

// The terminal joins a browser WebSocket to the node relay WebSocket and
// records open and close audit events for the platform view.
func TestTerminalRelayWritesAudit(t *testing.T) {
	server, store, cookie, _, token, generation := terminalServer(t)
	dialer := websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, HandshakeTimeout: 5 * time.Second}
	base := "wss" + strings.TrimPrefix(server.URL, "https")

	terminalURL := base + "/api/v1/nodes/remote/containers/" + terminalContainer + "/terminal?generation=" + itoa(generation) + "&cols=120&rows=40"
	browserConn, reply, err := dialer.Dial(terminalURL, http.Header{"Cookie": {cookie.Name + "=" + cookie.Value}})
	if err != nil {
		if reply != nil && reply.Body != nil {
			reply.Body.Close()
		}
		t.Fatalf("browser dial: %v", err)
	}
	defer browserConn.Close()

	agentConn, _, err := dialer.Dial(base+"/agent/v1/exec-stream", http.Header{"Authorization": {"Bearer " + token}})
	if err != nil {
		t.Fatalf("agent dial: %v", err)
	}
	defer agentConn.Close()

	read := func(conn *websocket.Conn, name string) (int, []byte) {
		t.Helper()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		kind, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("%s read: %v", name, err)
		}
		return kind, data
	}
	write := func(conn *websocket.Conn, kind int, data []byte, name string) {
		t.Helper()
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := conn.WriteMessage(kind, data); err != nil {
			t.Fatalf("%s write: %v", name, err)
		}
	}

	// Handshake: parameters reach the node, the confirmation reaches the browser.
	kind, data := read(agentConn, "agent params")
	var params execstream.OpenParams
	if kind != websocket.TextMessage || json.Unmarshal(data, &params) != nil ||
		params.Container != terminalContainer || params.Cols != 120 || params.Rows != 40 {
		t.Fatalf("params kind=%d %s", kind, data)
	}
	write(agentConn, websocket.TextMessage, []byte(`{"ok":true}`), "agent ack")
	kind, data = read(browserConn, "browser ready")
	var ready struct {
		Ready bool   `json:"ready"`
		Error string `json:"error"`
	}
	if kind != websocket.TextMessage || json.Unmarshal(data, &ready) != nil || !ready.Ready {
		t.Fatalf("ready kind=%d %s", kind, data)
	}

	// Terminal bytes flow in both directions.
	write(browserConn, websocket.BinaryMessage, []byte("echo hi\r"), "browser input")
	kind, data = read(agentConn, "agent input")
	if kind != websocket.BinaryMessage || string(data) != "echo hi\r" {
		t.Fatalf("input kind=%d %q", kind, data)
	}
	write(agentConn, websocket.BinaryMessage, []byte("hi\r\n"), "agent output")
	kind, data = read(browserConn, "browser output")
	if kind != websocket.BinaryMessage || string(data) != "hi\r\n" {
		t.Fatalf("output kind=%d %q", kind, data)
	}
	write(agentConn, websocket.TextMessage, []byte(`{"eof":true,"exit_code":0}`), "agent eof")
	kind, data = read(browserConn, "browser eof")
	if kind != websocket.TextMessage || !strings.Contains(string(data), `"eof":true`) {
		t.Fatalf("eof kind=%d %s", kind, data)
	}

	// Both audit events land once the session closes (newest first).
	deadline := time.Now().Add(5 * time.Second)
	for {
		page, err := store.ReadProjectAuditBefore(t.Context(), cookie.Value, "", 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) >= 2 && page.Items[0].Action == "container.terminal.closed" && page.Items[1].Action == "container.terminal" {
			closed, opened := page.Items[0], page.Items[1]
			if opened.ActorName != "admin" || opened.Subject != "remote" ||
				!strings.Contains(opened.Detail, terminalContainer[:12]) {
				t.Fatalf("open event %+v", opened)
			}
			if !strings.Contains(closed.Detail, "closed after") {
				t.Fatalf("close event %+v", closed)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("audit events missing: %+v", page.Items)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

// Guard failures answer with HTTP errors before any WebSocket upgrade.
func TestTerminalGuards(t *testing.T) {
	server, _, admin, _, _, _ := terminalServer(t)
	memberLogin := httptest.NewRequest("POST", "https://control/api/v1/auth/login", strings.NewReader(`{"username":"member","password":"member test password"}`))
	memberLogin.Header.Set("Content-Type", "application/json")
	memberLogin.Header.Set("Origin", "https://control")
	rec := httptest.NewRecorder()
	server.Config.Handler.ServeHTTP(rec, memberLogin)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("member login: %d %s", rec.Code, rec.Body)
	}
	member := rec.Result().Cookies()[0]

	terminalPath := "/api/v1/nodes/remote/containers/" + terminalContainer + "/terminal?generation=1&cols=120&rows=40"
	invoke := func(cookie *http.Cookie, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "https://control"+path, nil)
		req.Header.Set("Origin", "https://control")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		res := httptest.NewRecorder()
		server.Config.Handler.ServeHTTP(res, req)
		return res
	}
	if res := invoke(nil, terminalPath); res.Code != http.StatusUnauthorized && res.Code != http.StatusForbidden {
		t.Fatalf("anonymous: %d %s", res.Code, res.Body)
	}
	if res := invoke(member, terminalPath); res.Code != http.StatusForbidden {
		t.Fatalf("member: %d %s", res.Code, res.Body)
	}
	if res := invoke(admin, "/api/v1/nodes/remote/containers/not-a-container/terminal?generation=1"); res.Code != http.StatusBadRequest {
		t.Fatalf("bad container: %d %s", res.Code, res.Body)
	}
	if res := invoke(admin, "/api/v1/nodes/remote/containers/"+terminalContainer+"/terminal?generation=1&cols=1"); res.Code != http.StatusBadRequest {
		t.Fatalf("bad cols: %d %s", res.Code, res.Body)
	}
	if res := invoke(admin, "/api/v1/nodes/remote/containers/"+terminalContainer+"/terminal"); res.Code != http.StatusBadRequest {
		t.Fatalf("missing generation: %d %s", res.Code, res.Body)
	}
	if res := invoke(admin, "/api/v1/nodes/@local/containers/"+terminalContainer+"/terminal?generation=1"); res.Code != http.StatusServiceUnavailable {
		t.Fatalf("local node: %d %s", res.Code, res.Body)
	}
}

// Exhausting the broker's session slots answers with 429.
func TestTerminalCap(t *testing.T) {
	server, _, admin, broker, _, _ := terminalServer(t)
	for i := 0; i < execstream.MaxSessions; i++ {
		session, err := broker.Begin("remote", 1, execstream.OpenParams{Container: terminalContainer})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(session.Cancel)
	}
	req := httptest.NewRequest("GET", "https://control/api/v1/nodes/remote/containers/"+terminalContainer+"/terminal?generation=1", nil)
	req.Header.Set("Origin", "https://control")
	req.AddCookie(admin)
	res := httptest.NewRecorder()
	server.Config.Handler.ServeHTTP(res, req)
	if res.Code != http.StatusTooManyRequests {
		t.Fatalf("capped: %d %s", res.Code, res.Body)
	}
}

// A node that rejects the exec surfaces its error in the browser handshake.
func TestTerminalAckFailure(t *testing.T) {
	server, _, cookie, _, token, generation := terminalServer(t)
	dialer := websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, HandshakeTimeout: 5 * time.Second}
	base := "wss" + strings.TrimPrefix(server.URL, "https")
	browserConn, _, err := dialer.Dial(base+"/api/v1/nodes/remote/containers/"+terminalContainer+"/terminal?generation="+itoa(generation), http.Header{"Cookie": {cookie.Name + "=" + cookie.Value}})
	if err != nil {
		t.Fatalf("browser dial: %v", err)
	}
	defer browserConn.Close()
	agentConn, _, err := dialer.Dial(base+"/agent/v1/exec-stream", http.Header{"Authorization": {"Bearer " + token}})
	if err != nil {
		t.Fatalf("agent dial: %v", err)
	}
	defer agentConn.Close()
	_ = agentConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, err := agentConn.ReadMessage(); err != nil {
		t.Fatal(err)
	}
	_ = agentConn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := agentConn.WriteMessage(websocket.TextMessage, []byte(`{"ok":false,"error":"no usable shell in container"}`)); err != nil {
		t.Fatal(err)
	}
	_ = browserConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	kind, data, err := browserConn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var ready struct {
		Ready bool   `json:"ready"`
		Error string `json:"error"`
	}
	if kind != websocket.TextMessage || json.Unmarshal(data, &ready) != nil || ready.Ready || ready.Error != "no usable shell in container" {
		t.Fatalf("failure notice kind=%d %s", kind, data)
	}
}

type completedLocalTerminal struct{ *strings.Reader }

func (completedLocalTerminal) Write(p []byte) (int, error)            { return len(p), nil }
func (completedLocalTerminal) Resize(context.Context, int, int) error { return nil }
func (completedLocalTerminal) ExitCode(context.Context) int           { return 0 }
func (completedLocalTerminal) Close() error                           { return nil }

func TestLocalContainerTerminalUsesDockerStarterAndAudits(t *testing.T) {
	server, store, admin, _, _, _ := terminalServer(t, func(_ context.Context, id string, cols, rows int) (execstream.Terminal, error) {
		if id != terminalContainer || cols != 101 || rows != 29 {
			t.Errorf("unexpected terminal request: %s %d %d", id, cols, rows)
		}
		return completedLocalTerminal{strings.NewReader("from container")}, nil
	})
	dialer := websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, HandshakeTimeout: 5 * time.Second}
	endpoint := "wss" + strings.TrimPrefix(server.URL, "https") + "/api/v1/nodes/@local/containers/" + terminalContainer + "/terminal?generation=1&cols=101&rows=29"
	conn, _, err := dialer.Dial(endpoint, http.Header{"Cookie": {admin.Name + "=" + admin.Value}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for _, want := range []struct {
		kind int
		text string
	}{
		{websocket.TextMessage, `{"ready":true}`},
		{websocket.BinaryMessage, "from container"},
		{websocket.TextMessage, `{"eof":true}`},
	} {
		kind, data, err := conn.ReadMessage()
		if err != nil || kind != want.kind || string(data) != want.text {
			t.Fatalf("terminal frame: %d %q %v", kind, data, err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		page, err := store.ReadProjectAuditBefore(t.Context(), admin.Value, "", 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) >= 2 && page.Items[0].Action == "container.terminal.closed" && page.Items[1].Action == "container.terminal" {
			if page.Items[1].Subject != storage.LocalExecutorID {
				t.Fatalf("wrong terminal node: %+v", page.Items[1])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("terminal audit missing: %+v", page.Items)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
