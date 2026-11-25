package imagearchive

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestRelayCancelsBlockedPeer(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	receiver, stop := context.WithCancel(ctx)
	defer stop()
	r := new(Relay)
	reader, writer := io.Pipe()
	defer writer.Close()
	sendDone := make(chan error, 1)
	receiveDone := make(chan error, 1)
	ready := make(chan struct{})
	go func() { sendDone <- r.Send(ctx, "transfer", reader) }()
	go func() {
		receiveDone <- r.Receive(receiver, "transfer", func(reader io.Reader) error {
			close(ready)
			_, err := io.Copy(io.Discard, reader)
			return err
		})
	}()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("transfer did not rendezvous")
	}
	stop()
	if err := <-receiveDone; err == nil {
		t.Fatal("cancelled receiver reported success")
	}
	if err := <-sendDone; err == nil {
		t.Fatal("cancelled receiver did not stop sender")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.transfers) != 0 {
		t.Fatal("cancelled transfer left live state")
	}
}

func TestArchiveReaderRejectsAnExtraByte(t *testing.T) {
	src := &archiveReader{Reader: strings.NewReader("data!"), remaining: 4}
	out, err := io.ReadAll(src)
	if string(out) != "data" || !errors.Is(err, ErrTooLarge) {
		t.Fatal("archive reader did not enforce its bound")
	}
	if _, err := io.ReadAll(src); !errors.Is(err, ErrTooLarge) {
		t.Fatal("archive transfer recovered from a bound violation")
	}
}

func TestTLSArchiveClientStreamsBothDirections(t *testing.T) {
	id := strings.Repeat("a", 32)
	data := bytes.Repeat([]byte("test archive bytes\n"), 64<<10)
	relay := new(Relay)
	gateway := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent/v1/operations/"+id+"/image-archive" || r.Header.Get("Authorization") != "Bearer test-agent-credential" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch r.Method {
		case http.MethodGet:
			ReceiveHTTP(w, r, relay, id, "")
		case http.MethodPost:
			SendHTTP(w, r, relay, id)
		}
	}))
	defer gateway.Close()
	ca := x509.NewCertPool()
	ca.AddCert(gateway.Certificate())
	endpoint, err := url.Parse(gateway.URL)
	if err != nil {
		t.Fatal(err)
	}
	endpoint.Scheme = "ws"
	if _, err := NewClient(endpoint, nil, "test-agent-credential"); err == nil {
		t.Fatal("unencrypted Agent endpoint could receive archive bytes")
	}
	endpoint.Scheme = "wss"
	client, err := NewClient(endpoint, &tls.Config{RootCAs: ca}, "test-agent-credential")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	check := func(reader io.Reader) error {
		got, err := io.ReadAll(reader)
		if err != nil {
			return err
		}
		if !bytes.Equal(got, data) {
			return errors.New("archive changed in transit")
		}
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- relay.Receive(ctx, id, check) }()
	if err := client.Send(ctx, id, io.NopCloser(bytes.NewReader(data))); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	go func() { done <- relay.Send(ctx, id, io.NopCloser(bytes.NewReader(data))) }()
	if err := client.Receive(ctx, id, check); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRelayAbortsBeforeTransferSourceIsOpened(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	r := new(Relay)
	done := make(chan error, 1)
	go func() {
		done <- r.Receive(ctx, "missing-docker-source", func(io.Reader) error {
			return errors.New("unexpected archive")
		})
	}()
	for ctx.Err() == nil {
		r.mu.Lock()
		waiting := len(r.transfers) == 1
		r.mu.Unlock()
		if waiting {
			r.Abort("missing-docker-source")
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err := <-done; !errors.Is(err, ErrUnavailable) {
		t.Fatalf("failed operation did not promptly release the download: %v", err)
	}
}
