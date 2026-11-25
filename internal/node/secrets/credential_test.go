package secrets

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
)

func TestCredentialSecretGatewayAndPrivateHTTP(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil || r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("X-Moori-Snapshot-Digest") != "sha256:"+strings.Repeat("a", 64) {
			http.Error(w, "denied", 401)
			return
		}
		w.Write([]byte(`{"PASSWORD":"c2VjcmV0"}`))
	}))
	defer backend.Close()
	u, _ := url.Parse(backend.URL)
	proxy := httptest.NewTLSServer(httputil.NewSingleHostReverseProxy(u))
	defer proxy.Close()
	roots := x509.NewCertPool()
	roots.AddCert(proxy.Certificate())
	for _, address := range []string{"ws" + strings.TrimPrefix(backend.URL, "http"), "wss" + strings.TrimPrefix(proxy.URL, "https")} {
		u, _ := url.Parse(address)
		c, e := NewCredentialClient(u, &tls.Config{RootCAs: roots}, "test-token")
		if e != nil {
			t.Fatal(e)
		}
		values, e := c.Fetch(context.Background(), "execution", "sha256:"+strings.Repeat("a", 64))
		c.Close()
		if e != nil || string(values["PASSWORD"]) != "secret" {
			t.Fatalf("%v %v", values, e)
		}
	}
}
