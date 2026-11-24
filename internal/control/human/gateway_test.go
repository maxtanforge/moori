package human

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maxtanforge/moori/internal/control/storage"
)

func TestGatewaySessionUsesBrowserOrigin(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.CreateHumanUser(ctx, "admin", "initial test password"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	backend := httptest.NewServer(mux)
	defer backend.Close()
	upstream, _ := url.Parse(backend.URL)
	proxy := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		r.SetURL(upstream)
		r.Out.Host = r.In.Host
		r.SetXForwarded()
	}}
	gateway := httptest.NewTLSServer(proxy)
	defer gateway.Close()
	Mount(mux, s, ViewOptions{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}})
	client := gateway.Client()
	client.Jar, _ = cookiejar.New(nil)
	invoke := func(origin, site string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest("POST", gateway.URL+"/api/v1/auth/login", strings.NewReader(`{"username":"admin","password":"initial test password"}`))
		req.Header.Set("Origin", origin)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Sec-Fetch-Site", site)
		req.Header.Set("X-Forwarded-Proto", "http")
		req.Header.Set("X-Forwarded-Host", "evil.example")
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { res.Body.Close() })
		return res
	}
	if res := invoke("https://evil.example", "cross-site"); res.StatusCode != 403 {
		t.Fatalf("cross-site login accepted: %d", res.StatusCode)
	}
	res := invoke(gateway.URL, "same-origin")
	if res.StatusCode != 204 {
		t.Fatalf("proxy login: %d", res.StatusCode)
	}
	cookies := res.Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || cookies[0].Name != cookieName {
		t.Fatalf("gateway cookie: %+v", cookies)
	}
	res, err = client.Get(gateway.URL + "/api/v1/auth/me")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("proxy session: %d", res.StatusCode)
	}
}

func TestBrowserTransportAndOrigin(t *testing.T) {
	for _, tc := range []struct {
		name, host, origin, peer, forwarded, site string
		want                                      int
		secure                                    bool
	}{
		{"loopback", "127.0.0.1:8080", "http://127.0.0.1:8080", "127.0.0.1:1234", "", "same-origin", 204, false},
		{"remote HTTP", "192.0.2.20:8080", "http://192.0.2.20:8080", "192.0.2.30:1234", "", "", 204, false},
		{"HTTP proxy", "moori.example:80", "http://moori.example", "127.0.0.1:1234", "http", "same-origin", 204, false},
		{"forwarded HTTP with loopback Host", "localhost", "http://localhost", "127.0.0.1:1234", "http", "same-origin", 204, false},
		{"trusted gateway", "moori.example:443", "https://moori.example", "127.0.0.1:1234", "https", "same-origin", 204, true},
		{"browser HTTPS with rewritten Host", "moori:8080", "https://moori.example", "192.0.2.30:1234", "", "same-origin", 204, true},
		{"forwarded scheme alone does not set a Secure cookie", "moori.example", "", "192.0.2.30:1234", "https", "", 204, false},
		{"cross-site browser metadata", "moori.example", "https://moori.example", "192.0.2.30:1234", "", "cross-site", 403, false},
		{"other origin without browser metadata", "moori.example", "https://other.example", "127.0.0.1:1234", "https", "", 403, false},
		{"same-site sibling", "moori.example", "https://other.example", "127.0.0.1:1234", "https", "same-site", 403, false},
		{"HTTPS origin without browser metadata", "moori.example", "https://moori.example", "127.0.0.1:1234", "", "", 204, true},
		{"wrong port without browser metadata", "moori.example:8080", "https://moori.example:8443", "127.0.0.1:1234", "", "", 403, false},
		{"opaque origin without browser metadata", "127.0.0.1", "null", "127.0.0.1:1234", "", "", 403, false},
		{"Docker published loopback", "localhost:8080", "http://localhost:8080", "172.18.0.1:1234", "", "same-origin", 204, false},
		{"untrusted forwarding does not enable Secure", "moori.example:8080", "http://moori.example:8080", "192.0.2.30:1234", "https", "same-origin", 204, false},
		{"cross-origin HTTP", "moori.example:8080", "http://other.example:8080", "192.0.2.30:1234", "", "", 403, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s, err := storage.Open(ctx, filepath.Join(t.TempDir(), "control.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if _, err := s.CreateHumanUser(ctx, "admin", "initial test password"); err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			Mount(mux, s, ViewOptions{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}})
			invoke := func(method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
				req := httptest.NewRequest(method, "http://"+tc.host+path, strings.NewReader(body))
				req.RemoteAddr = tc.peer
				if method == http.MethodPost {
					req.Header.Set("Origin", tc.origin)
					if tc.site != "" {
						req.Header.Set("Sec-Fetch-Site", tc.site)
					}
					req.Header.Set("Content-Type", "application/json")
				}
				if tc.forwarded != "" {
					req.Header.Set("X-Forwarded-Proto", tc.forwarded)
				}
				if cookie != nil {
					req.AddCookie(cookie)
				}
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, req)
				return w
			}
			w := invoke("POST", "/api/v1/auth/login", `{"username":"admin","password":"initial test password"}`, nil)
			if w.Code != tc.want {
				t.Fatalf("login: got %d want %d (%s)", w.Code, tc.want, w.Body.String())
			}
			cookies := w.Result().Cookies()
			if tc.want != 204 {
				if len(cookies) != 0 {
					t.Fatalf("unexpected session cookie: %+v", cookies)
				}
				return
			}
			if len(cookies) != 1 || cookies[0].Secure != tc.secure || (cookies[0].Name == cookieName) != tc.secure {
				t.Fatalf("session cookie: %+v", cookies)
			}
			if w = invoke("GET", "/api/v1/auth/me", "", cookies[0]); w.Code != 200 {
				t.Fatalf("me: %d", w.Code)
			}
			w = invoke("POST", "/api/v1/auth/logout", "", cookies[0])
			cleared := w.Result().Cookies()
			if w.Code != 204 || len(cleared) != 1 || cleared[0].Name != cookies[0].Name || cleared[0].MaxAge != -1 {
				t.Fatalf("logout: %d %+v", w.Code, cleared)
			}
		})
	}
}
