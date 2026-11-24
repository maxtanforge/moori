package human

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maxtanforge/moori/internal/control/storage"
)

// Alert policy and notification targets are platform configuration: the
// endpoints answer administrators with the effective policy (defaults until
// customized) and manage webhook destinations.
func TestAlertSettingsEndpoints(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(ctx, filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, name := range []string{"member", "admin"} {
		if _, err := s.CreateHumanUser(ctx, name, name+" test password"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetPlatformAdmin(ctx, "admin", true); err != nil {
		t.Fatal(err)
	}
	router := http.NewServeMux()
	Mount(router, s, ViewOptions{})
	invoke := func(method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "https://control"+path, strings.NewReader(body))
		req.Header.Set("Origin", "https://control")
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		return res
	}
	login := func(username, password string) *http.Cookie {
		t.Helper()
		res := invoke("POST", "/api/v1/auth/login", `{"username":"`+username+`","password":"`+password+`"}`, nil)
		if res.Code != http.StatusNoContent {
			t.Fatalf("login %s: %d %s", username, res.Code, res.Body)
		}
		return res.Result().Cookies()[0]
	}
	member := login("member", "member test password")
	admin := login("admin", "admin test password")

	if res := invoke("GET", "/api/v1/alert-settings", "", member); res.Code != http.StatusForbidden {
		t.Fatalf("member settings: %d %s", res.Code, res.Body)
	}
	res := invoke("GET", "/api/v1/alert-settings", "", admin)
	if res.Code != http.StatusOK {
		t.Fatalf("admin settings: %d %s", res.Code, res.Body)
	}
	var view struct {
		Enabled          bool   `json:"enabled"`
		Custom           bool   `json:"custom"`
		CPUPercent       int    `json:"cpu_percent"`
		MemoryPercent    int    `json:"memory_percent"`
		SustainedSeconds int    `json:"sustained_seconds"`
		Targets          int    `json:"targets"`
		UpdatedBy        string `json:"updated_by"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Enabled || view.Custom || view.CPUPercent != 90 || view.MemoryPercent != 90 ||
		view.SustainedSeconds != 120 || view.Targets != 0 || view.UpdatedBy != "" {
		t.Fatalf("default settings view = %+v", view)
	}

	if res := invoke("PUT", "/api/v1/alert-settings", `{"cpu_percent":0}`, admin); res.Code != http.StatusBadRequest {
		t.Fatalf("invalid settings accepted: %d %s", res.Code, res.Body)
	}
	if res := invoke("PUT", "/api/v1/alert-settings", `{"cpu_percent":95,"memory_percent":92,"sustained_seconds":180,"updated_by":"x"}`, admin); res.Code != http.StatusBadRequest {
		t.Fatalf("unknown field accepted: %d %s", res.Code, res.Body)
	}
	if res := invoke("PUT", "/api/v1/alert-settings", `{"cpu_percent":95,"memory_percent":92,"sustained_seconds":180}`, admin); res.Code != http.StatusNoContent {
		t.Fatalf("save settings: %d %s", res.Code, res.Body)
	}
	res = invoke("GET", "/api/v1/alert-settings", "", admin)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"custom":true`) ||
		!strings.Contains(res.Body.String(), `"sustained_seconds":180`) ||
		!strings.Contains(res.Body.String(), `"updated_by":"admin"`) {
		t.Fatalf("customized settings: %d %s", res.Code, res.Body)
	}

	if res := invoke("POST", "/api/v1/alert-targets", `{"url":"ftp://x"}`, admin); res.Code != http.StatusBadRequest {
		t.Fatalf("invalid target accepted: %d %s", res.Code, res.Body)
	}
	if res := invoke("POST", "/api/v1/alert-targets", `{"url":"https://hooks.example.com/a"}`, member); res.Code != http.StatusForbidden {
		t.Fatalf("member target create: %d %s", res.Code, res.Body)
	}
	res = invoke("POST", "/api/v1/alert-targets", `{"url":"  https://hooks.example.com/a  "}`, admin)
	if res.Code != http.StatusOK {
		t.Fatalf("create target: %d %s", res.Code, res.Body)
	}
	var target struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &target); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(target.ID, "alert-") || len(target.ID) != len("alert-")+26 || target.URL != "https://hooks.example.com/a" {
		t.Fatalf("target = %+v", target)
	}
	res = invoke("GET", "/api/v1/alert-targets", "", admin)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"items":[`) {
		t.Fatalf("list targets: %d %s", res.Code, res.Body)
	}
	if res := invoke("DELETE", "/api/v1/alert-targets/alert-missing", "", admin); res.Code != http.StatusNotFound {
		t.Fatalf("missing target delete: %d %s", res.Code, res.Body)
	}
	if res := invoke("DELETE", "/api/v1/alert-targets/"+target.ID, "", admin); res.Code != http.StatusNoContent {
		t.Fatalf("delete target: %d %s", res.Code, res.Body)
	}

	if res := invoke("DELETE", "/api/v1/alert-settings", "", member); res.Code != http.StatusForbidden {
		t.Fatalf("member clear: %d %s", res.Code, res.Body)
	}
	if res := invoke("DELETE", "/api/v1/alert-settings", "", admin); res.Code != http.StatusNoContent {
		t.Fatalf("clear settings: %d %s", res.Code, res.Body)
	}
	res = invoke("GET", "/api/v1/alert-settings", "", admin)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"custom":false`) {
		t.Fatalf("cleared settings: %d %s", res.Code, res.Body)
	}
}
