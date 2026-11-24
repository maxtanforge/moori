package human

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maxtanforge/moori/internal/control/storage"
)

func TestSecureLoginAndLogout(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(ctx, filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.CreateHumanUser(ctx, "member", "initial test password"); err != nil {
		t.Fatal(err)
	}
	router := http.NewServeMux()
	Mount(router, s, ViewOptions{})
	invoke := func(method, path, origin, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "https://control"+path, strings.NewReader(body))
		req.Header.Set("Origin", origin)
		req.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		return res
	}
	const login = "/api/v1/auth/login"
	const credentials = `{"username":"member","password":"initial test password"}`
	if res := invoke("POST", login, "https://elsewhere", credentials, nil); res.Code != http.StatusForbidden {
		t.Fatalf("cross-origin login: %d", res.Code)
	}
	res := invoke("POST", login, "https://control", credentials, nil)
	cookies := res.Result().Cookies()
	if res.Code != http.StatusNoContent || len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("unsecured login: %d %v", res.Code, cookies)
	}
	if res := invoke("GET", "/api/v1/auth/me", "", "", cookies[0]); res.Code != http.StatusOK {
		t.Fatalf("session refresh: %d", res.Code)
	}
	for _, endpoint := range []struct{ method, path string }{
		{"GET", "/api/v1/reviews/pending"},
		{"POST", "/api/v1/projects/project/deployments/deploy/review"},
	} {
		if res := invoke(endpoint.method, endpoint.path, "https://control", "", cookies[0]); res.Code != http.StatusNotFound {
			t.Fatalf("removed route %s: %d", endpoint.path, res.Code)
		}
	}
	if res := invoke("POST", "/api/v1/auth/logout", "https://control", "", cookies[0]); res.Code != http.StatusNoContent {
		t.Fatalf("logout failed: %d", res.Code)
	}
	if res := invoke("GET", "/api/v1/auth/me", "", "", cookies[0]); res.Code != http.StatusUnauthorized {
		t.Fatalf("revoked cookie accepted: %d", res.Code)
	}
}

// Node operations reject with ErrTargetBusy when the node already shows an
// active execution or has a queued operation; browsers must see the business
// state as a conflict, not an internal failure.
func TestWriteErrorMapsTargetBusyToConflict(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"bare", storage.ErrTargetBusy},
		{"wrapped", fmt.Errorf("%w: Agent evidence may still show active execution", storage.ErrTargetBusy)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			writeError(recorder, tc.err)
			if recorder.Code != http.StatusConflict {
				t.Fatalf("writeError status = %d, want %d", recorder.Code, http.StatusConflict)
			}
			if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
				t.Fatalf("writeError content type = %q, want JSON", got)
			}
			var envelope struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil || envelope.Code != "target_busy" || envelope.Message == "" {
				t.Fatalf("writeError envelope = %q (%v)", recorder.Body.String(), err)
			}
			recorder = httptest.NewRecorder()
			writeNodeOperationError(recorder, tc.err)
			if recorder.Code != http.StatusConflict {
				t.Fatalf("writeNodeOperationError status = %d, want %d", recorder.Code, http.StatusConflict)
			}
		})
	}
}
