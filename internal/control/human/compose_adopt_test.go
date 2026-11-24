package human

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/control/storage"
)

func TestComposeAdoptAPIConvertsAndFeedsTemplates(t *testing.T) {
	db, err := storage.Open(t.Context(), filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	session, err := db.InitializeAdmin(t.Context(), "admin", "adopt password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	Mount(mux, db, ViewOptions{})
	call := func(method, path, body string, status int, authenticated bool) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "https://control.example"+path, strings.NewReader(body))
		req.Header.Set("Origin", "https://control.example")
		req.Header.Set("Content-Type", "application/json")
		if authenticated {
			req.AddCookie(&http.Cookie{Name: cookieName, Value: session})
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != status {
			t.Fatalf("%s %s got %d, want %d (%s)", method, path, w.Code, status, w.Body)
		}
		return w
	}
	call(http.MethodPost, "/api/v1/compose-adopt", `{}`, http.StatusUnauthorized, false)
	call(http.MethodPost, "/api/v1/compose-adopt", `{"project":"bad name!","compose":"services: {}"}`, http.StatusBadRequest, true)

	digest := "sha256:" + strings.Repeat("a", 64)
	request := map[string]any{
		"project": "myapp",
		"compose": "services:\n  web:\n    image: nginx:1.27\n    ports: [\"8080:80\"]\n    volumes: [\"data:/data\"]\nvolumes:\n  data: {}\n",
		"pins":    map[string]any{"web": []string{"nginx@" + digest}},
	}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	response := call(http.MethodPost, "/api/v1/compose-adopt", string(raw), http.StatusOK, true)
	var adopted struct {
		Compose string `json:"compose"`
		Issues  []struct {
			Service string `json:"service"`
			Message string `json:"message"`
		} `json:"issues"`
		Services []string `json:"services"`
		Volumes  []string `json:"volumes"`
		Networks []string `json:"networks"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &adopted); err != nil {
		t.Fatal(err)
	}
	if adopted.Compose == "" || len(adopted.Services) != 1 || len(adopted.Issues) != 0 ||
		len(adopted.Volumes) != 1 || adopted.Volumes[0] != "myapp_data" || !strings.Contains(adopted.Compose, "image: nginx@"+digest) {
		t.Fatalf("unexpected adoption result: %+v", adopted)
	}
	// The converted document must be savable as a console template verbatim.
	save, err := json.Marshal(storage.ConsoleTemplateInput{Name: "纳管 myapp", Compose: adopted.Compose})
	if err != nil {
		t.Fatal(err)
	}
	call(http.MethodPost, "/api/v1/compose-templates", string(save), http.StatusCreated, true)
}
