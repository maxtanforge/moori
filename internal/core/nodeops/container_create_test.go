package nodeops

import (
	"encoding/json"
	"testing"
)

func TestStandaloneContainerConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"pull by registry ID", `{"action":"container.create","name":"media","edit":{"image":"app:v1","pull_scope":{"registry_id":"private"}}}`, true},
		{"missing pull registry", `{"action":"container.create","name":"media","edit":{"image":"app:v1","pull_scope":{"project":"team"}}}`, false},
		{"pull local ID", `{"action":"container.create","name":"media","edit":{"image":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","pull_scope":{"project":"team","registry_id":"private"}}}`, false},
		{"pull short local ID", `{"action":"container.create","name":"media","edit":{"image":"abcdefabcdef","pull_scope":{"project":"team","registry_id":"private"}}}`, false},
		{"create", `{"action":"container.create","name":"media","edit":{"image":"nginx:alpine","binds":["/srv/media:/media:ro"],"volumes":["media-cache:/cache"]}}`, true},
		{"inspect edit configuration", `{"action":"container.edit-config","target":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, true},
		{"invalid name", `{"action":"container.create","name":"a/b","edit":{"image":"nginx:alpine"}}`, false},
		{"missing image", `{"action":"container.create","name":"media","edit":{}}`, false},
		{"reserved label", `{"action":"container.create","name":"media","edit":{"image":"nginx:alpine","labels":{"Com.Docker.Compose.Project":"stack"}}}`, false},
		{"relative bind", `{"action":"container.create","name":"media","edit":{"image":"nginx:alpine","binds":["srv/media:/media"]}}`, false},
		{"noncanonical bind", `{"action":"container.create","name":"media","edit":{"image":"nginx:alpine","binds":["/srv/../data:/media"]}}`, false},
		{"conflicting mount target", `{"action":"container.create","name":"media","edit":{"image":"nginx:alpine","binds":["/srv/media:/media"],"volumes":["media-cache:/media"]}}`, false},
		{"bind option injection", `{"action":"container.create","name":"media","edit":{"image":"nginx:alpine","binds":["/srv/media:/media:rw,shared"]}}`, false},
		{"privileged input", `{"action":"container.create","name":"media","project":"team","edit":{"image":"nginx:alpine"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var req Request
			if err := json.Unmarshal([]byte(tc.body), &req); err != nil {
				t.Fatal(err)
			}
			if err := req.Validate(); (err == nil) != tc.valid {
				t.Fatalf("validate = %v, want valid=%t", err, tc.valid)
			}
		})
	}
}
