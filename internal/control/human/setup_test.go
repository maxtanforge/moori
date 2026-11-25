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

func TestSetupHTTPOriginAndSession(t *testing.T) {
	s, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	mux := http.NewServeMux()
	Mount(mux, s, ViewOptions{})
	post := func(origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "https://control/api/v1/auth/setup", strings.NewReader(`{"username":"admin","password":"initial password"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	if w := post("https://other"); w.Code != 403 {
		t.Fatal(w.Code)
	}
	w := post("https://control")
	if w.Code != 204 || len(w.Result().Cookies()) != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	cookie := w.Result().Cookies()[0]
	req := httptest.NewRequest("GET", "https://control/api/v1/auth/me", nil)
	req.AddCookie(cookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"username":"admin"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := post("https://control"); w.Code != 409 {
		t.Fatal(w.Code)
	}
}

func TestPasswordWorkRejectsOverlappingRequest(t *testing.T) {
	s, err := storage.Open(t.Context(), filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	mux := http.NewServeMux()
	Mount(mux, s, ViewOptions{})

	post := func(w http.ResponseWriter, path, body string, cookie *http.Cookie) {
		r := httptest.NewRequest(http.MethodPost, "https://control/api/v1/auth/"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "https://control")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		mux.ServeHTTP(w, r)
	}
	const credentials = `{"username":"admin","password":"initial password"}`
	reply, unblock := context.WithCancel(t.Context())
	setup := &heldPasswordResponse{
		ResponseRecorder: httptest.NewRecorder(),
		writing:          make(chan struct{}),
		resume:           reply.Done(),
	}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		post(setup, "setup", credentials, nil)
	}()
	defer func() {
		unblock()
		<-finished
	}()
	select {
	case <-setup.writing:
	case <-finished:
		t.Fatal("setup finished before writing a response")
	}

	login := httptest.NewRecorder()
	post(login, "login", credentials, nil)
	if login.Code != http.StatusTooManyRequests || login.Header().Get("Retry-After") != "1" {
		t.Fatalf("overlapping login = %d, Retry-After = %q", login.Code, login.Header().Get("Retry-After"))
	}

	unblock()
	<-finished
	cookies := setup.Result().Cookies()
	if setup.Code != http.StatusNoContent || len(cookies) != 1 {
		t.Fatalf("setup = %d, cookies = %d", setup.Code, len(cookies))
	}
	account := httptest.NewRecorder()
	post(account, "account", `{"username":"admin","current":"initial password","replacement":"updated password"}`, cookies[0])
	if account.Code != http.StatusNoContent {
		t.Fatalf("account change after setup = %d (%s)", account.Code, account.Body)
	}
	login = httptest.NewRecorder()
	post(login, "login", `{"username":"admin","password":"updated password"}`, nil)
	if login.Code != http.StatusNoContent {
		t.Fatalf("login after account change = %d (%s)", login.Code, login.Body)
	}
}

type heldPasswordResponse struct {
	*httptest.ResponseRecorder
	writing chan struct{}
	resume  <-chan struct{}
}

func (w *heldPasswordResponse) WriteHeader(status int) {
	close(w.writing)
	<-w.resume
	w.ResponseRecorder.WriteHeader(status)
}
