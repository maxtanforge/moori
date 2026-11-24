package ci

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
	"github.com/maxtanforge/moori/internal/platform/execlog"
)

func TestPublishAndReadTargetArtifactOverHTTP(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.RegisterTarget(ctx, "team", "test"); err != nil {
		t.Fatal(err)
	}
	token, err := s.IssuePublishingCredential(ctx, "publisher", "team", "test", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h := Handler{Store: s}
	mux.HandleFunc("POST /api/v1/releases", h.Publish)
	mux.HandleFunc("GET /api/v1/projects/{project}/targets/{target}/releases/{release}", h.ReadRelease)
	body, err := json.Marshal(storage.Publication{Project: "team", Target: "test", ID: "r1",
		Compose: "services: {web: {image: 'nginx@sha256:" + strings.Repeat("a", 64) + "'}}"})
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, data string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "https://control"+path, strings.NewReader(data))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		mux.ServeHTTP(res, req)
		return res
	}
	first := call("POST", "/api/v1/releases", string(body))
	read := call("GET", "/api/v1/projects/team/targets/test/releases/r1", "")
	if first.Code != 200 || read.Code != 200 || first.Body.String() != read.Body.String() {
		t.Fatalf("publication and lookup disagree: %d %s / %d %s", first.Code, first.Body, read.Code, read.Body)
	}
	if crossed := call("GET", "/api/v1/projects/team/targets/prod/releases/r1", ""); crossed.Code != 404 {
		t.Fatalf("release escaped target scope: %d", crossed.Code)
	}
	logs, err := execlog.Open(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	deploy, err := s.AdmitPublishedRelease(ctx, token, storage.Admission{ID: "deploy", Project: "team", Target: "test", Release: "r1", Key: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Claim(ctx, deploy, "attempt", "agent", 1, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := logs.Put(execlog.Entry{Execution: "attempt", Text: "bounded output", Truncated: true}); err != nil {
		t.Fatal(err)
	}
	mux.HandleFunc("GET /api/v1/deployments/{id}/logs", Handler{Store: s, Logs: logs}.Output)
	path := "/api/v1/deployments/deploy/logs"
	if res := call("GET", path, ""); res.Code != 200 || !strings.Contains(res.Body.String(), "bounded output") || !strings.Contains(res.Body.String(), `"truncated":true`) {
		t.Fatalf("owner could not read output: %d %s", res.Code, res.Body)
	}
	other, err := s.IssueServiceCredential(ctx, "other", "team", "test", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "https://control"+path, nil)
	request.Header.Set("Authorization", "Bearer "+other)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != 404 {
		t.Fatalf("another credential read the output: %d", response.Code)
	}
}
