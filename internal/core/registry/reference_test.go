package registry

import "testing"

func TestRegistryImageScope(t *testing.T) {
	for _, alias := range []string{"Docker.io", "index.docker.io", "registry-1.docker.io"} {
		if got, err := NormalizeHost(alias); err != nil || got != "docker.io" {
			t.Fatalf("host %q: %q, %v", alias, got, err)
		}
	}
	if _, err := NormalizeHost("https://registry.example"); err == nil {
		t.Fatal("scheme accepted")
	}
	got, err := ParseImage("registry.example:5000/team/app:release")
	if err != nil || got.Host != "registry.example:5000" || !InRepository(got.Repository, "team") || InRepository(got.Repository, "te") {
		t.Fatalf("scope validation: %+v %v", got, err)
	}
}
