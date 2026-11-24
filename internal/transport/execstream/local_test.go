package execstream

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type localTestTerminal struct {
	*io.PipeReader
	input chan string
	sizes chan Size
}

func (t *localTestTerminal) Write(data []byte) (int, error) {
	t.input <- string(data)
	return len(data), nil
}
func (t *localTestTerminal) Resize(_ context.Context, cols, rows int) error {
	t.sizes <- Size{Cols: cols, Rows: rows}
	return nil
}
func (t *localTestTerminal) ExitCode(context.Context) int { return 7 }

func TestLocalTerminalFrames(t *testing.T) {
	broker := &Broker{}
	session, err := broker.Begin("@local", 1, OpenParams{Container: testContainer})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Cancel()
	reader, writer := io.Pipe()
	defer writer.Close()
	terminal := &localTestTerminal{reader, make(chan string, 1), make(chan Size, 1)}
	done := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		done <- session.ConnectLocal(r.Context(), NewConnection(conn), func(_ context.Context, target string, cols, rows int) (Terminal, error) {
			if target != testContainer || cols != ColDefault || rows != RowDefault {
				t.Errorf("unexpected open: %s %d %d", target, cols, rows)
			}
			return terminal, nil
		})
	}))
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if kind, msg := readFrame(t, conn); kind != websocket.TextMessage || string(msg) != `{"ready":true}` {
		t.Fatalf("unexpected ready: %d %s", kind, msg)
	}
	writeFrame(t, conn, websocket.BinaryMessage, []byte("whoami\n"))
	writeFrame(t, conn, websocket.TextMessage, []byte(`{"resize":{"cols":96,"rows":28}}`))
	select {
	case input := <-terminal.input:
		if input != "whoami\n" {
			t.Fatal(input)
		}
	case <-time.After(time.Second):
		t.Fatal("input not delivered")
	}
	select {
	case size := <-terminal.sizes:
		if size != (Size{Cols: 96, Rows: 28}) {
			t.Fatal(size)
		}
	case <-time.After(time.Second):
		t.Fatal("resize not delivered")
	}
	go func() { _, _ = writer.Write([]byte("root\r\n")); _ = writer.Close() }()
	if kind, msg := readFrame(t, conn); kind != websocket.BinaryMessage || string(msg) != "root\r\n" {
		t.Fatalf("unexpected output: %d %s", kind, msg)
	}
	if kind, msg := readFrame(t, conn); kind != websocket.TextMessage || string(msg) != `{"eof":true,"exit_code":7}` {
		t.Fatalf("unexpected exit: %d %s", kind, msg)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal did not close")
	}
	select {
	case <-session.Started():
	default:
		t.Fatal("terminal did not confirm startup")
	}
	if broker.slots != 0 {
		t.Fatalf("slot leaked: %d", broker.slots)
	}
}
