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
	"github.com/maxtanforge/moori/internal/core/deployment"
	"github.com/maxtanforge/moori/internal/core/release"
)

func TestSubmissionUsesTargetPolicyAndRequiresScopedBearer(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(ctx, filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.RegisterTarget(ctx, "project", "target"); err != nil {
		t.Fatal(err)
	}
	r, err := release.Compile(ctx, []byte("services: {api: {image: 'nginx@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'}}"), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PublishRelease(ctx, "project", "release", r); err != nil {
		t.Fatal(err)
	}
	token, err := s.IssueServiceCredential(ctx, "ci", "project", "target", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	endpoint := Handler{Store: s}
	call := func(token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "https://control/api/v1/deployments", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Idempotency-Key", "workflow-1")
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		endpoint.ServeHTTP(res, req)
		return res
	}
	body := `{"project":"project","target":"target","release":"release"}`
	if bad := call("fake", body); bad.Code != http.StatusUnauthorized {
		t.Fatalf("bad credential: %d", bad.Code)
	}
	if bad := call(token, `{"project":"project","target":"outside","release":"release"}`); bad.Code != http.StatusUnauthorized {
		t.Fatalf("wrong target: %d", bad.Code)
	}
	var id string
	for range 2 {
		res := call(token, body)
		if res.Code != http.StatusAccepted {
			t.Fatalf("submit: %d %s", res.Code, res.Body)
		}
		var reply struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(res.Body.Bytes(), &reply); err != nil || reply.ID == "" {
			t.Fatalf("response: %s %v", res.Body, err)
		}
		if id != "" && reply.ID != id {
			t.Fatal("replay returned another deployment")
		}
		id = reply.ID
	}
	view, err := s.Get(ctx, id)
	if err != nil || view.State != deployment.Queued {
		t.Fatalf("target policy was bypassed: %+v %v", view, err)
	}
	router := http.NewServeMux()
	router.Handle("GET /api/v1/deployments/{id}", endpoint)
	lookup := func(credential string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "https://control/api/v1/deployments/"+id, nil)
		req.Header.Set("Authorization", "Bearer "+credential)
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		return res
	}
	if reply := lookup(token); reply.Code != http.StatusOK || !strings.Contains(reply.Body.String(), "queued") {
		t.Fatalf("status unavailable to request owner: %d %s", reply.Code, reply.Body)
	}
	other, err := s.IssueServiceCredential(ctx, "other", "project", "target", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if reply := lookup(other); reply.Code != http.StatusNotFound {
		t.Fatalf("another credential could view the deployment: %d", reply.Code)
	}
	router.Handle("POST /api/v1/deployments/{id}/cancel", endpoint)
	cancel := func(credential string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "https://control/api/v1/deployments/"+id+"/cancel", nil)
		req.Header.Set("Authorization", "Bearer "+credential)
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		return res
	}
	if reply := cancel(other); reply.Code != http.StatusNotFound {
		t.Fatalf("another credential canceled: %d", reply.Code)
	}
	if reply := cancel(token); reply.Code != http.StatusOK || !strings.Contains(reply.Body.String(), "canceled") {
		t.Fatalf("owner cancellation: %d %s", reply.Code, reply.Body)
	}
	if err := s.AssignTarget(ctx, "target", "agent", 1); err != nil {
		t.Fatal(err)
	}
	source, err := s.AdmitPublishedRelease(ctx, token, storage.Admission{
		ID: "previous", Project: "project", Target: "target", Release: "release", Key: "previous",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.NextOffer(ctx, "agent", 1, time.Now(), time.Minute); err != nil {
		t.Fatal(err)
	}
	for i, state := range []deployment.State{deployment.Executing, deployment.Verifying, deployment.Succeeded} {
		if err := s.ApplyEvent(ctx, storage.Event{ID: string(state), Execution: source, Agent: "agent", Generation: 1, Sequence: int64(i + 1), State: state}); err != nil {
			t.Fatal(err)
		}
	}
	router.HandleFunc("POST /api/v1/deployments/{id}/rollback", endpoint.Rollback)
	rollback := func(credential string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "https://control/api/v1/deployments/"+source+"/rollback", strings.NewReader(`{"note":"rollback after incident"}`))
		req.Header.Set("Authorization", "Bearer "+credential)
		req.Header.Set("Idempotency-Key", "rollback-1")
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		return res
	}
	if reply := rollback(other); reply.Code != http.StatusNotFound {
		t.Fatalf("foreign credential used rollback source: %d", reply.Code)
	}
	first, again := rollback(token), rollback(token)
	if first.Code != http.StatusAccepted || again.Code != http.StatusAccepted || first.Body.String() != again.Body.String() {
		t.Fatalf("rollback is not idempotent: %d %s / %d %s", first.Code, first.Body, again.Code, again.Body)
	}
}
