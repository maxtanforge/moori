package agentwire

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
)

func TestCredentialThroughTLSGatewayAndHTTP(t *testing.T) {
	h := Handler{Authenticate: func(r *http.Request) (Peer, error) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			return Peer{}, errors.New("denied")
		}
		return Peer{ID: "node", Generation: 1}, nil
	}, Serve: func(ctx context.Context, p Peer, c *Channel) { c.Send(Message{Kind: "hello", Body: []byte(`{}`)}) }}
	backend := httptest.NewServer(h)
	defer backend.Close()
	u, _ := url.Parse(backend.URL)
	gateway := httptest.NewTLSServer(httputil.NewSingleHostReverseProxy(u))
	defer gateway.Close()
	roots := x509.NewCertPool()
	roots.AddCert(gateway.Certificate())
	for _, target := range []string{"ws" + strings.TrimPrefix(backend.URL, "http"), "wss" + strings.TrimPrefix(gateway.URL, "https")} {
		c, e := DialCredential(context.Background(), target, &tls.Config{RootCAs: roots}, "test-token")
		if e != nil {
			t.Fatal(e)
		}
		_, e = c.Receive()
		c.Close()
		if e != nil {
			t.Fatal(e)
		}
		if c, e := DialCredential(context.Background(), target, &tls.Config{RootCAs: roots}, "bad"); e == nil {
			c.Close()
			t.Fatal("bad token accepted")
		}
	}
}
