package agentwire

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCredentialAuthenticatesChannelAndBoundsFrames(t *testing.T) {
	failure := make(chan error, 1)
	h := Handler{
		Authenticate: func(r *http.Request) (Peer, error) {
			if r.Header.Get("Authorization") != "Bearer test-token" {
				return Peer{}, errors.New("denied")
			}
			return Peer{ID: "agent", Generation: 2}, nil
		},
		Serve: func(_ context.Context, peer Peer, c *Channel) {
			m, err := c.Receive()
			if err == nil {
				err = c.Send(Message{Kind: m.Kind, Body: json.RawMessage(`{"id":"` + peer.ID + `"}`)})
			}
			failure <- err
		},
	}
	s := httptest.NewUnstartedServer(h)
	s.StartTLS()
	defer s.Close()
	trust := x509.NewCertPool()
	trust.AddCert(s.Certificate())
	target := "wss" + strings.TrimPrefix(s.URL, "https")
	clientTLS := &tls.Config{RootCAs: trust, MinVersion: tls.VersionTLS12}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if c, err := DialCredential(ctx, target, clientTLS, "wrong-token"); err == nil {
		c.Close()
		t.Fatal("accepted invalid credential")
	}
	c, err := DialCredential(ctx, target, clientTLS, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Send(Message{Kind: "oversize", Body: json.RawMessage(`"` + strings.Repeat("x", maxFrame) + `"`)}); err == nil {
		t.Fatal("oversized frame accepted")
	}
	if err := c.Send(Message{Kind: "hello", Body: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	m, err := c.Receive()
	if err != nil || m.Kind != "hello" || string(m.Body) != `{"id":"agent"}` {
		t.Fatalf("reply=%+v err=%v", m, err)
	}
	if err := <-failure; err != nil {
		t.Fatal(err)
	}
}
