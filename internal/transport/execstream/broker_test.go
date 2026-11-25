package execstream

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

const testContainer = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type testHarness struct {
	broker  *Broker
	session *Session
	server  *httptest.Server
	accepts chan error
}

func shortenTimeouts(t *testing.T) {
	t.Helper()
	openTimeout, ackTimeout = 300*time.Millisecond, 300*time.Millisecond
	t.Cleanup(func() { openTimeout, ackTimeout = 5*time.Second, 10*time.Second })
}

func newHarness(t *testing.T) *testHarness {
	t.Helper()
	h := &testHarness{broker: new(Broker), accepts: make(chan error, 4)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /agent", func(w http.ResponseWriter, r *http.Request) {
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		h.accepts <- h.broker.Accept(r.Context(), "remote", int64(1), NewConnection(c))
	})
	mux.HandleFunc("GET /browser", func(w http.ResponseWriter, r *http.Request) {
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_ = h.session.Connect(r.Context(), NewConnection(c))
	})
	h.server = httptest.NewTLSServer(mux)
	t.Cleanup(h.server.Close)
	return h
}

func (h *testHarness) dial(t *testing.T, path string) *websocket.Conn {
	t.Helper()
	dialer := websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, HandshakeTimeout: 5 * time.Second}
	conn, reply, err := dialer.Dial("wss"+strings.TrimPrefix(h.server.URL, "https")+path, nil)
	if err != nil {
		if reply != nil && reply.Body != nil {
			reply.Body.Close()
		}
		t.Fatalf("dial %s: %v", path, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func readFrame(t *testing.T, conn *websocket.Conn) (int, []byte) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	kind, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	return kind, data
}

func writeFrame(t *testing.T, conn *websocket.Conn, kind int, data []byte) {
	t.Helper()
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteMessage(kind, data); err != nil {
		t.Fatalf("write frame: %v", err)
	}
}

func TestBridgeRelaysTerminalFrames(t *testing.T) {
	shortenTimeouts(t)
	h := newHarness(t)
	session, err := h.broker.Begin("remote", 1, OpenParams{Container: testContainer, Cols: 120, Rows: 40})
	if err != nil {
		t.Fatal(err)
	}
	h.session = session

	// The node relay receives exec parameters and confirms them.
	agent := h.dial(t, "/agent")
	kind, data := readFrame(t, agent)
	if kind != websocket.TextMessage {
		t.Fatalf("params frame kind %d", kind)
	}
	var params OpenParams
	if err := json.Unmarshal(data, &params); err != nil {
		t.Fatal(err)
	}
	if params.Container != testContainer || params.Cols != 120 || params.Rows != 40 {
		t.Fatalf("unexpected params %+v", params)
	}
	writeFrame(t, agent, websocket.TextMessage, []byte(`{"ok":true}`))

	// The browser learns the session is ready and exchanges terminal bytes.
	browser := h.dial(t, "/browser")
	kind, data = readFrame(t, browser)
	if kind != websocket.TextMessage {
		t.Fatalf("ready frame kind %d", kind)
	}
	var ready readyMessage
	if err := json.Unmarshal(data, &ready); err != nil || !ready.Ready {
		t.Fatalf("ready frame %s err=%v", data, err)
	}
	writeFrame(t, browser, websocket.BinaryMessage, []byte("ls -l\r"))
	writeFrame(t, browser, websocket.TextMessage, []byte(`{"resize":{"cols":100,"rows":30}}`))
	kind, data = readFrame(t, agent)
	if kind != websocket.BinaryMessage || string(data) != "ls -l\r" {
		t.Fatalf("input frame kind=%d data=%q", kind, data)
	}
	kind, data = readFrame(t, agent)
	if kind != websocket.TextMessage || string(data) != `{"resize":{"cols":100,"rows":30}}` {
		t.Fatalf("resize frame kind=%d data=%s", kind, data)
	}
	writeFrame(t, agent, websocket.BinaryMessage, []byte("output\r\n"))
	kind, data = readFrame(t, browser)
	if kind != websocket.BinaryMessage || string(data) != "output\r\n" {
		t.Fatalf("output frame kind=%d data=%q", kind, data)
	}
	writeFrame(t, agent, websocket.TextMessage, []byte(`{"eof":true,"exit_code":0}`))
	kind, data = readFrame(t, browser)
	if kind != websocket.TextMessage || !strings.Contains(string(data), `"eof":true`) {
		t.Fatalf("eof frame kind=%d data=%s", kind, data)
	}
	select {
	case err := <-h.accepts:
		if err != nil {
			t.Fatalf("Accept returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Accept did not finish")
	}
	if !selectClosed(session.Started()) {
		t.Fatal("session never started")
	}
}

func selectClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func TestAckFailureReachesTheBrowser(t *testing.T) {
	shortenTimeouts(t)
	h := newHarness(t)
	session, err := h.broker.Begin("remote", 1, OpenParams{Container: testContainer})
	if err != nil {
		t.Fatal(err)
	}
	h.session = session
	agent := h.dial(t, "/agent")
	readFrame(t, agent) // parameters
	writeFrame(t, agent, websocket.TextMessage, []byte(`{"ok":false,"error":"no usable shell in container"}`))
	browser := h.dial(t, "/browser")
	kind, data := readFrame(t, browser)
	var ready readyMessage
	if kind != websocket.TextMessage || json.Unmarshal(data, &ready) != nil || ready.Ready || ready.Error != "no usable shell in container" {
		t.Fatalf("browser got kind=%d data=%s", kind, data)
	}
	select {
	case err := <-h.accepts:
		if err == nil || err.Error() != "no usable shell in container" {
			t.Fatalf("Accept returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Accept did not finish")
	}
	if selectClosed(session.Started()) {
		t.Fatal("failed session must not start")
	}
}

func TestBeginValidatesParams(t *testing.T) {
	b := new(Broker)
	cases := []OpenParams{
		{Container: "short"},
		{Container: strings.ToUpper(testContainer)},
		{Container: testContainer, Cols: 1},
		{Container: testContainer, Cols: 2048},
		{Container: testContainer, Rows: 1},
		{Container: testContainer, Rows: 900},
	}
	for _, params := range cases {
		if _, err := b.Begin("remote", 1, params); err != ErrInvalid {
			t.Fatalf("Begin %+v returned %v", params, err)
		}
	}
	session, err := b.Begin("remote", 1, OpenParams{Container: testContainer})
	if err != nil {
		t.Fatal(err)
	}
	if session.Cols != ColDefault || session.Rows != RowDefault {
		t.Fatalf("Normalize produced %dx%d", session.Cols, session.Rows)
	}
}

func TestBeginRejectsWhenSlotsAreFull(t *testing.T) {
	b := new(Broker)
	for i := 0; i < MaxSessions; i++ {
		if _, err := b.Begin("remote", 1, OpenParams{Container: testContainer}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.Begin("remote", 1, OpenParams{Container: testContainer}); err != ErrBusy {
		t.Fatalf("expected ErrBusy, got %v", err)
	}
}

func TestCancelFreesTheSlot(t *testing.T) {
	b := new(Broker)
	for i := 0; i < MaxSessions; i++ {
		session, err := b.Begin("remote", 1, OpenParams{Container: testContainer})
		if err != nil {
			t.Fatal(err)
		}
		session.Cancel()
	}
	if _, err := b.Begin("remote", 1, OpenParams{Container: testContainer}); err != nil {
		t.Fatalf("Cancel did not free slots: %v", err)
	}
}

func TestConnectTimesOutWithoutANode(t *testing.T) {
	shortenTimeouts(t)
	h := newHarness(t)
	session, err := h.broker.Begin("remote", 1, OpenParams{Container: testContainer})
	if err != nil {
		t.Fatal(err)
	}
	h.session = session
	browser := h.dial(t, "/browser")
	start := time.Now()
	kind, data := readFrame(t, browser)
	if kind != websocket.TextMessage {
		t.Fatalf("frame kind %d", kind)
	}
	var ready readyMessage
	if json.Unmarshal(data, &ready) != nil || ready.Ready || ready.Error == "" {
		t.Fatalf("expected a failure notice, got %s", data)
	}
	if elapsed := time.Since(start); elapsed < 200*time.Millisecond {
		t.Fatalf("Connect failed too early: %v", elapsed)
	}
}

func TestPeerMismatchDoesNotPair(t *testing.T) {
	shortenTimeouts(t)
	h := newHarness(t)
	session, err := h.broker.Begin("other", 7, OpenParams{Container: testContainer})
	if err != nil {
		t.Fatal(err)
	}
	h.session = session
	h.dial(t, "/agent") // registered for peer remote/1, not other/7
	browser := h.dial(t, "/browser")
	kind, data := readFrame(t, browser)
	var ready readyMessage
	if kind != websocket.TextMessage || json.Unmarshal(data, &ready) != nil || ready.Ready {
		t.Fatalf("expected pairing failure, got kind=%d %s", kind, data)
	}
}

func TestIdleRelayFramesEndTheConnection(t *testing.T) {
	h := newHarness(t)
	// No browser session: the relay connection must terminate on a data frame.
	agent := h.dial(t, "/agent")
	writeFrame(t, agent, websocket.TextMessage, []byte(`{"hello":true}`))
	select {
	case err := <-h.accepts:
		if err == nil {
			t.Fatal("Accept must fail on pre-pairing frames")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Accept did not finish")
	}
}

func TestDroppedRelayEndsTheConnection(t *testing.T) {
	h := newHarness(t)
	agent := h.dial(t, "/agent")
	_ = agent.Close()
	select {
	case err := <-h.accepts:
		if err == nil {
			t.Fatal("expected an error after the relay dropped")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Accept did not finish")
	}
}
