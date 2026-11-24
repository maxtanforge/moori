package gitea

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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

func TestPublishedReleaseIsAuthenticatedAndDeduplicated(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.RegisterTarget(ctx, "project", "prod"); err != nil {
		t.Fatal(err)
	}
	r, err := release.Compile(ctx, []byte("services: {web: {image: 'nginx@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'}}"), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PublishRelease(ctx, "project", "v1", r); err != nil {
		t.Fatal(err)
	}
	token, err := s.IssueServiceCredential(ctx, "hook", "project", "prod", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	const secret = "a-distinct-test-secret-at-least-32-bytes"
	bindings := []Binding{{ID: "production", RepositoryID: 10, Project: "project", Target: "prod", Secret: secret}}
	send := func(event, body, signingKey, delivery, bearer string) *httptest.ResponseRecorder {
		t.Helper()
		ingress, err := New(s, bindings)
		if err != nil {
			t.Fatal(err)
		}
		router := http.NewServeMux()
		router.Handle("POST /api/v1/webhooks/gitea/{hook}", ingress)
		req := httptest.NewRequest(http.MethodPost, "http://control/api/v1/webhooks/gitea/production", strings.NewReader(body))
		sig := hmac.New(sha256.New, []byte(signingKey))
		sig.Write([]byte(body))
		req.Header.Set("X-Gitea-Signature", hex.EncodeToString(sig.Sum(nil)))
		req.Header.Set("X-Gitea-Event", event)
		req.Header.Set("X-Gitea-Delivery", delivery)
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		return res
	}
	const published = `{"action":"published","repository":{"id":10},"release":{"id":42,"tag_name":"v1"}}`
	for _, tc := range []struct {
		event, body, key, token string
		status                  int
	}{
		{"release", published, "incorrect", token, 401},
		{"release", published, secret, "invalid", 401},
		{"release", strings.Replace(published, `"id":10`, `"id":11`, 1), secret, token, 403},
		{"push", published, secret, token, 204},
		{"release", strings.Replace(published, "published", "updated", 1), secret, token, 204},
	} {
		res := send(tc.event, tc.body, tc.key, "ignored", tc.token)
		if res.Code != tc.status {
			t.Fatalf("event=%q: status=%d want %d body=%s", tc.event, res.Code, tc.status, res.Body)
		}
	}
	var id string
	for _, delivery := range []string{"attempt-1", "attempt-2"} {
		res := send("release", published, secret, delivery, token)
		if res.Code != 202 {
			t.Fatalf("publish: %d %s", res.Code, res.Body)
		}
		var reply struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(res.Body.Bytes(), &reply); err != nil || reply.ID == "" {
			t.Fatalf("reply: %+v %v", reply, err)
		}
		if id != "" && id != reply.ID {
			t.Fatal("duplicate produced another deployment")
		}
		id = reply.ID
	}
	view, err := s.Get(ctx, id)
	if err != nil || view.State != deployment.Queued {
		t.Fatalf("queued deployment: %+v %v", view, err)
	}
	replacement, err := s.IssueServiceCredential(ctx, "hook-next", "project", "prod", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeServiceCredential(ctx, "hook"); err != nil {
		t.Fatal(err)
	}
	res := send("release", published, secret, "after-rotation", replacement)
	if res.Code != 202 || !strings.Contains(res.Body.String(), id) {
		t.Fatalf("rotation changed the deployment: %d %s", res.Code, res.Body)
	}
}
