package execstream

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/gorilla/websocket"

	wire "github.com/maxtanforge/moori/internal/transport/execstream"
)

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// relayClient builds a client aimed at the test server. The production
// constructor rejects relaxed TLS, so the test dials with the exported
// constructor and then relaxes the dialer it owns.
func relayClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	u := mustURL(t, server.URL)
	u.Scheme = "wss"
	client, err := NewClient(u, nil, "token")
	if err != nil {
		t.Fatal(err)
	}
	client.dialer.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	return client
}

// fakeSession is a scriptable exec session: writes are recorded, reads come
// from a pipe the test controls.
type fakeSession struct {
	mu       sync.Mutex
	input    bytes.Buffer
	resizes  []wire.Size
	exitCode int
	reader   io.Reader
	writer   io.WriteCloser
}

func newFakeSession(exitCode int) *fakeSession {
	pr, pw := io.Pipe()
	return &fakeSession{reader: pr, writer: pw, exitCode: exitCode}
}

func (s *fakeSession) Read(p []byte) (int, error) { return s.reader.Read(p) }
func (s *fakeSession) Close() error               { return s.writer.Close() }
func (s *fakeSession) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.input.Write(p)
}
func (s *fakeSession) Resize(_ context.Context, cols, rows int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resizes = append(s.resizes, wire.Size{Cols: cols, Rows: rows})
	return nil
}
func (s *fakeSession) ExitCode(_ context.Context) int { return s.exitCode }

func readKind(t *testing.T, conn *websocket.Conn) (int, []byte) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	kind, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("control read: %v", err)
	}
	return kind, data
}

func readText(t *testing.T, conn *websocket.Conn) []byte {
	t.Helper()
	kind, data := readKind(t, conn)
	if kind != websocket.TextMessage {
		t.Fatalf("expected a text frame, got kind %d", kind)
	}
	return data
}

func writeText(t *testing.T, conn *websocket.Conn, payload any) {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("control write: %v", err)
	}
}

func readAck(t *testing.T, conn *websocket.Conn) wire.Ack {
	t.Helper()
	var ack wire.Ack
	if err := json.Unmarshal(readText(t, conn), &ack); err != nil {
		t.Fatalf("ack %v", err)
	}
	return ack
}

// The relay acknowledges valid parameters, feeds keystrokes into the exec
// session, resizes on request and reports eof with the exit code when the
// process ends.
func TestServeRelaysTerminalSession(t *testing.T) {
	const container = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var sessionMu sync.Mutex
	var live *fakeSession
	var finished sync.Once
	finishedCh := make(chan struct{})
	start := func(_ context.Context, name string, cols, rows int) (Session, error) {
		if name != container || cols != 120 || rows != 40 {
			t.Errorf("starter got container=%q cols=%d rows=%d", name, cols, rows)
		}
		s := newFakeSession(7)
		sessionMu.Lock()
		live = s
		sessionMu.Unlock()
		return s, nil
	}
	upgrades := websocket.Upgrader{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrades.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		writeText(t, conn, wire.OpenParams{Container: container, Cols: 120, Rows: 40})
		if ack := readAck(t, conn); !ack.OK {
			t.Errorf("ack error %q", ack.Error)
			return
		}
		// Keystrokes arrive as binary frames.
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := conn.WriteMessage(websocket.BinaryMessage, []byte("ls\r")); err != nil {
			t.Errorf("input write: %v", err)
			return
		}
		sessionMu.Lock()
		s := live
		sessionMu.Unlock()
		if s == nil {
			t.Error("starter did not record the exec session")
			return
		}
		// Output from the session arrives as binary frames.
		go func() {
			time.Sleep(100 * time.Millisecond)
			_, _ = s.writer.Write([]byte("out\r\n"))
		}()
		kind, data := readKind(t, conn)
		if kind != websocket.BinaryMessage || string(data) != "out\r\n" {
			t.Errorf("output kind=%d %q", kind, data)
			return
		}
		writeText(t, conn, wire.Resize{Resize: wire.Size{Cols: 100, Rows: 30}})
		// Ending the exec process produces an eof frame with the exit code.
		_ = s.writer.Close()
		eof := readText(t, conn)
		var frame wire.EOF
		if json.Unmarshal(eof, &frame) != nil || !frame.EOF || frame.ExitCode != 7 {
			t.Errorf("eof frame %s", eof)
		}
		finished.Do(func() { close(finishedCh) })
	})
	server := httptest.NewTLSServer(handler)
	defer server.Close()

	client := relayClient(t, server)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx, start) }()

	<-finishedCh
	cancel()
	if err := <-done; err == nil {
		t.Fatal("Run must report the canceled context")
	}

	// The relay may still be draining its input loop; poll briefly.
	sessionMu.Lock()
	s := live
	sessionMu.Unlock()
	if s == nil {
		t.Fatal("starter did not record the exec session")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.mu.Lock()
		input, resizes := s.input.String(), s.resizes
		s.mu.Unlock()
		if input == "ls\r" && len(resizes) == 1 && resizes[0].Cols == 100 && resizes[0].Rows == 30 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("session input %q resizes %v", input, resizes)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Invalid parameters are refused with an error acknowledgement and the
// relay redials for the next browser session.
func TestServeRefusesInvalidParametersAndRedials(t *testing.T) {
	const container = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	var hits atomic.Int32
	sessions := make(chan string, 4)
	enough := make(chan struct{})
	var stop sync.Once
	start := func(_ context.Context, name string, _ int, _ int) (Session, error) {
		sessions <- name
		return newFakeSession(0), nil
	}
	upgrades := websocket.Upgrader{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrades.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if hits.Add(1) == 1 {
			writeText(t, conn, wire.OpenParams{Container: "short"})
			if ack := readAck(t, conn); ack.OK || !strings.Contains(ack.Error, "invalid") {
				t.Errorf("refusal ack ok=%v error=%q", ack.OK, ack.Error)
			}
			_ = conn.Close()
			return
		}
		writeText(t, conn, wire.OpenParams{Container: container})
		if ack := readAck(t, conn); !ack.OK {
			t.Errorf("second ack error %q", ack.Error)
			return
		}
		<-sessions
		stop.Do(func() { close(enough) })
	})
	server := httptest.NewTLSServer(handler)
	defer server.Close()

	client := relayClient(t, server)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx, start) }()

	<-enough
	cancel()
	if err := <-done; err == nil {
		t.Fatal("Run must report the canceled context")
	}
	if hits.Load() < 2 {
		t.Fatalf("relay did not redial after a refused session: %d hits", hits.Load())
	}
}

func TestNewClientValidatesEndpoint(t *testing.T) {
	if _, err := NewClient(mustURL(t, "https://control.example"), nil, "token"); err == nil {
		t.Fatal("https endpoint accepted")
	}
	if _, err := NewClient(mustURL(t, "wss://control.example"), nil, ""); err == nil {
		t.Fatal("empty token accepted")
	}
	if _, err := NewClient(mustURL(t, "wss://control.example"), &tls.Config{InsecureSkipVerify: true}, "token"); err == nil {
		t.Fatal("insecure TLS accepted")
	}
	if _, err := NewClient(nil, nil, "token"); err == nil {
		t.Fatal("nil endpoint accepted")
	}
}

// fakeDocker scripts the Docker client subset exec sessions need.
type fakeDocker struct {
	mu        sync.Mutex
	createErr map[string]error
	created   []container.ExecOptions
	resize    container.ResizeOptions
	running   bool
	exitCode  int
}

func (f *fakeDocker) ContainerInspect(_ context.Context, containerID string) (container.InspectResponse, error) {
	// InspectResponse embeds *ContainerJSONBase; the promoted ID reads
	// through that pointer, so the fake must populate it.
	return container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{ID: containerID, State: &container.State{Running: true}}}, nil
}

func (f *fakeDocker) ContainerExecCreate(_ context.Context, _ string, config container.ExecOptions) (container.ExecCreateResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, config)
	if err := f.createErr[config.Cmd[0]]; err != nil {
		return container.ExecCreateResponse{}, err
	}
	return container.ExecCreateResponse{ID: "exec-1"}, nil
}

func (f *fakeDocker) ContainerExecAttach(_ context.Context, _ string, _ container.ExecAttachOptions) (types.HijackedResponse, error) {
	server, client := net.Pipe()
	return types.HijackedResponse{Conn: client, Reader: bufio.NewReader(server)}, nil
}

func (f *fakeDocker) ContainerExecResize(_ context.Context, _ string, options container.ResizeOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resize = options
	return nil
}

func (f *fakeDocker) ContainerExecInspect(_ context.Context, _ string) (container.ExecInspect, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return container.ExecInspect{Running: f.running, ExitCode: f.exitCode}, nil
}

// The docker session prefers bash, falls back to sh, resizes through the
// daemon and reports the exec result once its metadata settles.
func TestStartDockerSessionShellSelection(t *testing.T) {
	docker := &fakeDocker{}
	session, err := StartDockerSession(t.Context(), docker, strings.Repeat("a", 64), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(docker.created) != 1 || len(docker.created[0].Cmd) != 3 || docker.created[0].Cmd[0] != "/bin/sh" || !strings.Contains(docker.created[0].Cmd[2], "exec /bin/bash") {
		t.Fatalf("shell attempts %v", docker.created)
	}
	for _, opts := range docker.created {
		if !opts.Tty || !opts.AttachStdin || !opts.AttachStdout || !opts.AttachStderr || len(opts.Env) != 1 || opts.Env[0] != "TERM=xterm" {
			t.Fatalf("exec options %+v", opts)
		}
	}
	docker.mu.Lock()
	docker.running, docker.exitCode = false, 3
	docker.mu.Unlock()
	if code := session.ExitCode(t.Context()); code != 3 {
		t.Fatalf("exit code %d", code)
	}
	if err := session.Resize(t.Context(), 100, 30); err != nil {
		t.Fatal(err)
	}
	docker.mu.Lock()
	resize := docker.resize
	docker.mu.Unlock()
	if resize.Width != 100 || resize.Height != 30 {
		t.Fatalf("resize %+v", resize)
	}
	_ = session.Close()
}

func TestStartDockerSessionNoUsableShell(t *testing.T) {
	docker := &fakeDocker{createErr: map[string]error{"/bin/bash": context.DeadlineExceeded, "/bin/sh": context.DeadlineExceeded}}
	if _, err := StartDockerSession(t.Context(), docker, strings.Repeat("a", 64), 80, 24); err == nil || !strings.Contains(err.Error(), "no usable shell") {
		t.Fatalf("expected a shell error, got %v", err)
	}
	if _, err := StartDockerSession(t.Context(), nil, strings.Repeat("a", 64), 80, 24); err == nil {
		t.Fatal("nil docker accepted")
	}
}
