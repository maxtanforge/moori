package dispatch_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"github.com/maxtanforge/moori/internal/control/dispatch"
	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/transport/agentwire"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCredentialRotationAndRevocationFenceLiveGatewaySession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, e := storage.Open(ctx, filepath.Join(t.TempDir(), "db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	token, _, e := s.IssueAgentCredential(ctx, "node", time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	backend := httptest.NewServer(dispatch.Endpoint(ctx, s, dispatch.EndpointOptions{Authenticate: dispatch.AuthenticateCredential(s)}))
	defer backend.Close()
	u, _ := url.Parse(backend.URL)
	gateway := httptest.NewTLSServer(httputil.NewSingleHostReverseProxy(u))
	defer gateway.Close()
	roots := x509.NewCertPool()
	roots.AddCert(gateway.Certificate())
	cfg := &tls.Config{RootCAs: roots}
	target := "wss" + strings.TrimPrefix(gateway.URL, "https")
	heartbeat, _ := agentwire.Encode(agentwire.KindHeartbeat, struct{}{})
	for _, rotate := range []bool{true, false} {
		c, e := agentwire.DialCredential(ctx, target, cfg, token)
		if e != nil {
			t.Fatal(e)
		}
		if e = c.Send(heartbeat); e != nil {
			t.Fatal(e)
		}
		if _, e = c.Receive(); e != nil {
			t.Fatal(e)
		}
		old := token
		if rotate {
			token, _, e = s.IssueAgentCredential(ctx, "node", time.Hour)
		} else {
			e = s.RevokeAgentCredential(ctx, "node")
		}
		if e != nil {
			t.Fatal(e)
		}
		if e = c.Send(heartbeat); e == nil {
			if _, e = c.Receive(); e == nil {
				t.Fatal("invalidated connection still served")
			}
		}
		c.Close()
		if c, e := agentwire.DialCredential(ctx, target, cfg, old); e == nil {
			c.Close()
			t.Fatal("invalidated credential reconnected")
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Forwarded-Client-Cert", "fake")
	if _, e := dispatch.AuthenticateCredential(s)(r); e == nil {
		t.Fatal("forwarded identity accepted")
	}
}
