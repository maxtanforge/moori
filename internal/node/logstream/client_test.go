package logstream

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	wire "github.com/maxtanforge/moori/internal/transport/logstream"
)

func TestClientStreamsThroughVerifiedTLSAndClosesAnIdleSource(t *testing.T) {
	t.Run("http1", func(t *testing.T) { testIdleSource(t, false) })
	t.Run("http2", func(t *testing.T) { testIdleSource(t, true) })
}

func testIdleSource(t *testing.T, useHTTP2 bool) {
	t.Helper()
	id := strings.Repeat("a", 64)
	const ticket = "abcdef0123456789"
	const token = "fixture-agent-token"
	var issued atomic.Bool
	posted := make(chan string, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/agent/v1/log-stream/next":
			if !issued.CompareAndSwap(false, true) {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			_ = json.NewEncoder(w).Encode(wire.Ticket{ID: ticket, Container: id, Tail: 200})
		case r.Method == http.MethodPost && r.URL.Path == "/agent/v1/log-stream/"+ticket:
			if r.Header.Get("Content-Type") != "application/octet-stream" {
				w.WriteHeader(http.StatusUnsupportedMediaType)
				return
			}
			if r.ProtoMajor == 1 {
				if err := http.NewResponseController(w).EnableFullDuplex(); err != nil {
					t.Error(err)
					return
				}
			}
			var data [3]byte
			if _, err := io.ReadFull(r.Body, data[:]); err != nil {
				t.Error(err)
				return
			}
			posted <- string(data[:])
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	server.EnableHTTP2 = useHTTP2
	server.StartTLS()
	defer server.Close()
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	endpoint.Scheme = "wss"
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	client, err := NewClient(endpoint, &tls.Config{RootCAs: roots}, token)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	reader, writer := io.Pipe()
	source := &trackedSource{ReadCloser: reader, closed: make(chan struct{})}
	defer writer.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- client.Run(ctx, func(_ context.Context, target string, tail int, _, _ bool) (io.ReadCloser, error) {
			if target != id || tail != 200 {
				return nil, errors.New("incorrect ticket passed to Docker")
			}
			return source, nil
		})
	}()
	defer func() { cancel(); <-done }()
	initial := make(chan error, 1)
	go func() {
		_, err := writer.Write([]byte("abc"))
		initial <- err
	}()
	select {
	case data := <-posted:
		if data != "abc" {
			t.Fatalf("posted payload = %q", data)
		}
	case <-ctx.Done():
		t.Fatal("log bytes were not streamed before EOF")
	}
	if err := <-initial; err != nil {
		t.Fatal(err)
	}
	select {
	case <-source.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("source stayed open after broker completed")
	}
}

func TestClientRequiresVerifiedRemoteConnection(t *testing.T) {
	address, err := url.Parse("ws://moori.example.invalid/agent/v1/connect")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewClient(address, nil, "fixture-agent-token"); err == nil {
		t.Fatal("cleartext remote log stream enabled")
	}
	address.Scheme = "wss"
	if _, err := NewClient(address, &tls.Config{InsecureSkipVerify: true}, "fixture-agent-token"); err == nil {
		t.Fatal("remote log stream could disable certificate verification")
	}
}

type trackedSource struct {
	io.ReadCloser
	closed chan struct{}
	once   sync.Once
}

func (s *trackedSource) Close() error {
	s.once.Do(func() {
		_ = s.ReadCloser.Close()
		close(s.closed)
	})
	return nil
}

func TestClientBrokerCanCancelBeforeDockerOpensTheStream(t *testing.T) {
	var issued atomic.Bool
	brokerClosed := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if err := http.NewResponseController(w).EnableFullDuplex(); err != nil {
				t.Error(err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			close(brokerClosed)
		} else if issued.CompareAndSwap(false, true) {
			_ = json.NewEncoder(w).Encode(wire.Ticket{ID: "first", Container: strings.Repeat("a", 64), Tail: 100})
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	address, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	address.Scheme = "wss"
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	client, err := NewClient(address, &tls.Config{RootCAs: roots}, "fixture-agent-token")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithCancel(t.Context())
	finished, openingCanceled := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		_ = client.Run(ctx, func(ctx context.Context, _ string, _ int, _, _ bool) (io.ReadCloser, error) {
			<-ctx.Done()
			close(openingCanceled)
			return nil, ctx.Err()
		})
	}()
	defer func() { cancel(); <-finished }()
	select {
	case <-brokerClosed:
	case <-time.After(2 * time.Second):
		t.Fatal("agent did not start POST while Docker was still opening")
	}
	select {
	case <-openingCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("broker completion did not cancel the Docker open")
	}
}
