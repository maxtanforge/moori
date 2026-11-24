package dispatch

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/transport/execstream"
)

// The relay endpoint rejects unverified transports, unknown credentials and
// browser-style connections before any terminal session is bridged.
func TestExecStreamEndpointGuards(t *testing.T) {
	ctx := t.Context()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	token, _, err := store.IssueAgentCredential(ctx, "remote", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	broker := new(execstream.Broker)
	mux := http.NewServeMux()
	mux.Handle("GET /agent/v1/exec-stream", ExecStreamEndpoint(store, broker))
	tlsServer := httptest.NewTLSServer(mux)
	defer tlsServer.Close()
	plain := httptest.NewServer(mux)
	defer plain.Close()

	dial := func(server *httptest.Server, headers http.Header) error {
		t.Helper()
		dialer := websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, HandshakeTimeout: 5 * time.Second}
		scheme, rest := "wss", strings.TrimPrefix(server.URL, "https")
		if strings.HasPrefix(server.URL, "http://") {
			scheme, rest = "ws", strings.TrimPrefix(server.URL, "http")
		}
		conn, reply, err := dialer.Dial(scheme+rest+"/agent/v1/exec-stream", headers)
		if err == nil {
			_ = conn.Close()
		} else if reply != nil && reply.Body != nil {
			reply.Body.Close()
		}
		return err
	}

	if err := dial(plain, http.Header{"Authorization": {"Bearer " + token}}); err == nil || !strings.Contains(err.Error(), "bad handshake") {
		t.Fatalf("plain HTTP dial: %v", err)
	}
	if err := dial(tlsServer, nil); err == nil || !strings.Contains(err.Error(), "bad handshake") {
		t.Fatalf("anonymous dial: %v", err)
	}
	if err := dial(tlsServer, http.Header{"Authorization": {"Bearer wrong"}, "Origin": {"https://control"}}); err == nil || !strings.Contains(err.Error(), "bad handshake") {
		t.Fatalf("browser-style dial: %v", err)
	}

	// A valid relay connection is upgraded and claims nothing until a
	// browser session arrives; dropping it releases the handler.
	conn, _, err := (&websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, HandshakeTimeout: 5 * time.Second}).
		Dial("wss"+strings.TrimPrefix(tlsServer.URL, "https")+"/agent/v1/exec-stream", http.Header{"Authorization": {"Bearer " + token}})
	if err != nil {
		t.Fatalf("authenticated dial: %v", err)
	}
	_ = conn.Close()
}
