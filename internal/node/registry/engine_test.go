package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/maxtanforge/moori/internal/core/registry"
	"github.com/docker/docker/client"
)

func TestEngineRejectsErrorInSuccessfulPullResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, err := base64.URLEncoding.DecodeString(r.Header.Get("X-Registry-Auth"))
		var cfg registry.Credential
		if err != nil || json.Unmarshal(auth, &cfg) != nil || cfg.Username != "robot" || cfg.Password != "token" || cfg.ServerAddress != "registry.example" {
			t.Error("expected registry auth header")
		}
		if r.URL.Query().Get("tag") != "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
			t.Error("request did not pin the manifest digest")
		}
		_, _ = w.Write([]byte("{\"errorDetail\":{\"message\":\"sensitive upstream detail\"}}\n"))
	}))
	defer server.Close()
	docker, err := client.NewClientWithOpts(client.WithHost(server.URL), client.WithVersion("1.48"))
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	engine := Engine{client: docker}
	err = engine.Pull(context.Background(), "registry.example/team/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "", registry.Credential{Username: "robot", Password: "token", ServerAddress: "registry.example"})
	if err == nil || err.Error() != "Docker could not pull the registry image" {
		t.Fatalf("pull error: %v", err)
	}
}
