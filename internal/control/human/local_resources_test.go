package human

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maxtanforge/moori/internal/control/storage"
)

func mountLocalResources(t *testing.T, options ViewOptions) (*httptest.Server, *storage.Store) {
	t.Helper()
	s, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	mux := http.NewServeMux()
	Mount(mux, s, options)
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)
	return server, s
}

func adminSession(t *testing.T, server *httptest.Server) *http.Cookie {
	t.Helper()
	r := httptest.NewRequest("POST", server.URL+"/api/v1/auth/setup", strings.NewReader(`{"username":"admin","password":"initial password"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", server.URL)
	w := httptest.NewRecorder()
	server.Config.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatal(w.Code, w.Body.String())
	}
	return w.Result().Cookies()[0]
}
