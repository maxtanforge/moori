package human

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/control/ci"
	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/core/nodeops"
)

func TestContainerWebhookDeploysSuccessiveDigests(t *testing.T) {
	server, store := mountLocalResources(t, ViewOptions{LocalExecution: true})
	cookie := adminSession(t, server)
	mux := server.Config.Handler.(*http.ServeMux)
	ci.MountContainerWebhooks(mux, store)
	ctx := t.Context()
	if err := store.EnsureLocalExecutor(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordNodeOperationCapability(ctx, storage.LocalExecutorID, 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body, token, key string, admin bool, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, server.URL+path, strings.NewReader(body))
		if admin {
			r.AddCookie(cookie)
		}
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s = %d, expected %d: %s", method, path, w.Code, status, w.Body.String())
		}
		return w
	}
	const adminPath = "/api/v1/container-webhooks"
	entry, err := store.CreateHumanRegistry(ctx, cookie.Value, storage.SecretKey{1}, storage.RegistryInput{
		Name: "team", Host: "registry.example", RepositoryPrefix: "team", Username: "test-user", Password: "test-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	binding := `{"node":"@local","generation":1,"container":"web","repository":"registry.example/team/web","registry_id":"` + entry.ID + `"}`
	call(http.MethodPost, adminPath, strings.Replace(binding, "registry.example/team/", "registry.example/another/", 1), "", "", true, http.StatusConflict)
	call(http.MethodPost, adminPath, binding, "", "", false, http.StatusUnauthorized)
	w := call(http.MethodPost, adminPath, binding, "", "", true, http.StatusCreated)
	var access struct{ ID, Token string }
	if err := json.Unmarshal(w.Body.Bytes(), &access); err != nil || access.ID == "" || access.Token == "" {
		t.Fatal("binding credentials unavailable", err)
	}
	w = call(http.MethodGet, adminPath, "", "", "", true, http.StatusOK)
	if !strings.Contains(w.Body.String(), access.ID) || !strings.Contains(w.Body.String(), `"registry_name":"team"`) || strings.Contains(w.Body.String(), access.Token) {
		t.Fatal("binding listing lost the ID or registry name, or disclosed its token")
	}
	path := "/api/v1/webhooks/containers/" + access.ID
	version1 := "registry.example/team/web@sha256:" + strings.Repeat("a", 64)
	version2 := "registry.example/team/web@sha256:" + strings.Repeat("b", 64)
	payload := func(image string) string { return `{"image":"` + image + `"}` }
	call(http.MethodPost, path, payload(version1), "invalid", "release-1", false, http.StatusUnauthorized)
	call(http.MethodPost, path, payload("registry.example/team/web:latest"), access.Token, "release-1", false, http.StatusBadRequest)
	call(http.MethodPost, path, payload(strings.Replace(version1, "/web@", "/other@", 1)), access.Token, "release-1", false, http.StatusForbidden)
	submit := func(version, key string) string {
		t.Helper()
		w := call(http.MethodPost, path, payload(version), access.Token, key, false, http.StatusAccepted)
		var op nodeops.Operation
		if err := json.Unmarshal(w.Body.Bytes(), &op); err != nil || op.ID == "" || op.Error != "" {
			t.Fatal("deployment receipt unavailable", err)
		}
		return op.ID
	}
	id := submit(version1, "release-1")
	if again := submit(version1, "release-1"); id != again {
		t.Fatal("duplicate submission created a new operation")
	}
	call(http.MethodPost, path, payload(version2), access.Token, "release-1", false, http.StatusConflict)
	call(http.MethodPost, path, payload(version2), access.Token, "release-2", false, http.StatusConflict)
	req, _, err := store.NextNodeOperation(ctx, storage.LocalExecutorID, 1, time.Now())
	if err != nil || req.Operation != id || req.Image != version1 || req.Name != "web" || req.Action != nodeops.ContainerRelease {
		t.Fatal("unexpected claimed operation", req, err)
	}
	if err := store.CompleteNodeOperation(ctx, storage.LocalExecutorID, 1, id, nodeops.Succeeded, "", &nodeops.Result{Image: version1}, time.Now()); err != nil {
		t.Fatal(err)
	}
	w = call(http.MethodGet, path+"/"+id, "", access.Token, "", false, http.StatusOK)
	if !strings.Contains(w.Body.String(), `"state":"succeeded"`) {
		t.Fatal("the webhook status did not report success")
	}
	next := submit(version2, "release-2")
	// HTTPS terminates at the gateway and the upstream Host is rewritten.
	revocation := httptest.NewRequest(http.MethodDelete, "http://backend"+adminPath+"/"+access.ID, nil)
	revocation.Header.Set("Origin", server.URL)
	revocation.Header.Set("Sec-Fetch-Site", "same-origin")
	revocation.AddCookie(cookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, revocation)
	if w.Code != http.StatusNoContent {
		t.Fatalf("revocation through HTTPS gateway returned %d: %s", w.Code, w.Body.String())
	}
	call(http.MethodDelete, adminPath+"/"+access.ID, "", "", "", true, http.StatusNoContent)
	audit := call(http.MethodGet, "/api/v1/audit", "", "", "", true, http.StatusOK).Body.String()
	if strings.Count(audit, `"action":"webhook.created"`) != 1 ||
		strings.Count(audit, `"action":"webhook.revoked"`) != 1 ||
		!strings.Contains(audit, access.ID) || strings.Contains(audit, access.Token) {
		t.Fatal("expected one audit event per change without credentials")
	}
	call(http.MethodGet, path+"/"+next, "", access.Token, "", false, http.StatusUnauthorized)
	if _, _, err := store.NextNodeOperation(ctx, storage.LocalExecutorID, 1, time.Now()); err == nil {
		t.Fatal("revoked webhook operation remained dispatchable")
	}
}
