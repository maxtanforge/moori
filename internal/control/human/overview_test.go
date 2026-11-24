package human

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/core/release"
)

// The landing dashboard endpoint serves platform administrators only, keeps
// working without the metrics store (enabled=false), and reports deployment
// states the browser cannot derive itself.
func TestOverviewEndpoint(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(ctx, filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.CreateHumanUser(ctx, "member", "member test password"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateHumanUser(ctx, "admin", "initial test password"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPlatformAdmin(ctx, "admin", true); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterTarget(ctx, "project", "target"); err != nil {
		t.Fatal(err)
	}
	artifact, err := release.Compile(ctx, []byte("services: {api: {image: 'nginx@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'}}"), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PublishRelease(ctx, "project", "release", artifact); err != nil {
		t.Fatal(err)
	}
	snapshot, err := artifact.Bind(release.Binding{Target: "target", ConfigVersion: "test-v1", HealthTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdmitSnapshot(ctx, storage.Admission{ID: "deploy", Project: "project", Caller: "requester", Key: "key", Target: "target", Release: "release"}, snapshot); err != nil {
		t.Fatal(err)
	}
	router := http.NewServeMux()
	Mount(router, s, ViewOptions{})
	invoke := func(method, path, origin, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "https://control"+path, strings.NewReader(body))
		req.Header.Set("Origin", origin)
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
		res := invoke("POST", "/api/v1/auth/login", "https://control", `{"username":"`+username+`","password":"`+password+`"}`, nil)
		if res.Code != http.StatusNoContent {
			t.Fatalf("login %s: %d %s", username, res.Code, res.Body)
		}
		return res.Result().Cookies()[0]
	}
	member := login("member", "member test password")
	if res := invoke("GET", "/api/v1/overview", "", "", member); res.Code != http.StatusForbidden {
		t.Fatalf("member overview: %d %s", res.Code, res.Body)
	}
	admin := login("admin", "initial test password")
	res := invoke("GET", "/api/v1/overview", "", "", admin)
	if res.Code != http.StatusOK {
		t.Fatalf("admin overview: %d %s", res.Code, res.Body)
	}
	var view struct {
		Enabled bool `json:"enabled"`
		Nodes   struct {
			Total     int `json:"total"`
			Online    int `json:"online"`
			Offline   int `json:"offline"`
			NeverSeen int `json:"never_seen"`
			Disabled  int `json:"disabled"`
		} `json:"nodes"`
		Deployments struct {
			Active int `json:"active"`
		} `json:"deployments"`
		Alerts struct {
			Active      int `json:"active"`
			FiredWindow int `json:"fired_window"`
		} `json:"alerts"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Enabled {
		t.Fatal("overview enabled without a metrics store")
	}
	if view.Nodes.Total != 0 {
		t.Fatalf("unexpected nodes without local execution: %+v", view.Nodes)
	}
	if view.Deployments.Active != 1 {
		t.Fatalf("deployments = %+v, want one active deployment", view.Deployments)
	}
	if view.Alerts.Active != 0 || view.Alerts.FiredWindow != 0 {
		t.Fatalf("alerts = %+v", view.Alerts)
	}
}
