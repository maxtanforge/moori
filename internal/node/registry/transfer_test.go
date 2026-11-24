package registry

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/maxtanforge/moori/internal/core/registry"
	"github.com/docker/docker/client"
)

func TestPushValidatesLocalTagAndRegistryBeforeTransfer(t *testing.T) {
	id := "sha256:" + strings.Repeat("a", 64)
	var pushes atomic.Int32
	docker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/json") {
			_ = json.NewEncoder(w).Encode(map[string]string{"Id": id})
			return
		}
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/push") || r.URL.Query().Get("tag") != "ready" {
			t.Error("unexpected daemon operation")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		pushes.Add(1)
		encoded, err := base64.URLEncoding.DecodeString(r.Header.Get("X-Registry-Auth"))
		var credential registry.Credential
		if err != nil || json.Unmarshal(encoded, &credential) != nil || credential.ServerAddress != "registry.example" || credential.Username != "test-user" {
			t.Error("push did not use the authorized host credentials")
		}
		_, _ = w.Write([]byte("{\"status\":\"pushed\"}\n"))
	}))
	defer docker.Close()
	client, err := client.NewClientWithOpts(client.WithHost(docker.URL), client.WithVersion("1.48"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	engine := &Engine{client: client}
	credential := registry.Credential{ServerAddress: "registry.example", Username: "test-user", Password: "not-a-secret"}
	if engine.Push(t.Context(), "another.example/team/test:ready", id, credential) == nil || pushes.Load() != 0 {
		t.Fatal("credential was reused for another registry")
	}
	if engine.Push(t.Context(), "registry.example/team/test:ready", "sha256:"+strings.Repeat("b", 64), credential) == nil || pushes.Load() != 0 {
		t.Fatal("push did not verify the expected local image")
	}
	if err := engine.Push(t.Context(), "registry.example/team/test:ready", id, credential); err != nil || pushes.Load() != 1 {
		t.Fatalf("authorized push failed: %v", err)
	}
}
