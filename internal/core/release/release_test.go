package release

import (
	"context"
	"strings"
	"testing"
	"time"
)

const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestFullAndTemplateProduceSamePinnedRelease(t *testing.T) {
	ctx := context.Background()
	resolve := func(_ context.Context, image string) (string, error) {
		if image != "docker.io/library/nginx:stable" {
			t.Fatalf("unexpected resolver input %q", image)
		}
		return digest, nil
	}
	full, err := Compile(ctx, []byte("services: {web: {image: 'nginx:stable'}}"), nil, nil, resolve)
	if err != nil {
		t.Fatal(err)
	}
	template, err := Compile(ctx, []byte("services: {web: {image: 'nginx:old'}}"), map[string]string{"web": "nginx:stable"}, []string{"web"}, resolve)
	if err != nil {
		t.Fatal(err)
	}
	if full.Digest() != template.Digest() || full.Compose() != template.Compose() || !strings.Contains(full.Compose(), "docker.io/library/nginx@"+digest) {
		t.Fatalf("release does not contain the pinned image: %s / %s", full.Compose(), template.Compose())
	}
}

func TestCompileRejectsUnsupportedOrUnpinnedInput(t *testing.T) {
	ctx := context.Background()
	for _, input := range []string{
		"services: {web: {image: nginx, privileged: true}}",
		"services: {web: {image: nginx, volumes: ['/:/host']}}",
		"services: {web: {image: nginx, image: alpine}}",
		"services: {web: {image: nginx, command: ['${HOST_VAR}']}}",
		"services: {web: {image: nginx}}\n---\nservices: {extra: {image: alpine}}",
		"services: {web: {image: nginx}}",
	} {
		if _, err := Compile(ctx, []byte(input), nil, nil, nil); err == nil {
			t.Errorf("accepted %s", input)
		}
	}
	if _, err := Compile(ctx, []byte("services: {web: {image: 'nginx@"+digest+"'}}"), map[string]string{"web": "nginx:old"}, nil, nil); err == nil {
		t.Fatal("accepted override without permission")
	}
}

func TestDeploymentDigestBindsExecutionInput(t *testing.T) {
	r, err := Compile(context.Background(), []byte("services: {web: {image: 'nginx@"+digest+"'}}"), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	a := Binding{Target: "target", ConfigVersion: "cfg-1", HealthTimeout: time.Minute,
		Secrets: map[string]string{"DATABASE_PASSWORD": "v1", "API_TOKEN": "v2"}, SecretServices: map[string][]string{"web": {"DATABASE_PASSWORD", "API_TOKEN"}}}
	first, err := r.Bind(a)
	if err != nil {
		t.Fatal(err)
	}
	a.Secrets["API_TOKEN"] = "v3"
	second, err := r.Bind(a)
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest() == second.Digest() || !strings.Contains(first.JSON(), `"API_TOKEN":"v2"`) {
		t.Fatal("secret version was not immutably bound")
	}
}

func TestCompileMaintainedServiceSettings(t *testing.T) {
	ctx := context.Background()
	source := "services:\n  web:\n    image: nginx@sha256:" + strings.Repeat("a", 64) +
		"\n    restart: 'on-failure:3'\n    labels: {team: frontend}\n    environment: {MODE: production}\n" +
		"    ports: ['8080:80/tcp']\n    volumes: ['data:/var/lib/app:ro']\n    networks: [backend]\n" +
		"volumes: {data: {}}\nnetworks: {backend: {}}\n"
	artifact, err := Compile(ctx, []byte(source), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`"restart":"on-failure:3"`, `"labels":{"team":"frontend"}`, `"MODE":"production"`,
		"8080:80/tcp", "data:/var/lib/app:ro", `"networks":["backend"]`} {
		if !strings.Contains(artifact.Compose(), expected) {
			t.Errorf("missing %s in %s", expected, artifact.Compose())
		}
	}
	for _, invalid := range []string{strings.Replace(source, "on-failure:3", "unexpected", 1),
		strings.Replace(source, "team:", "com.docker.compose.project:", 1)} {
		if _, err := Compile(ctx, []byte(invalid), nil, nil, nil); err == nil {
			t.Fatal("accepted invalid setting")
		}
	}
}

func TestExistingComposeAcceptsTagsAndExplicitKeyValueLists(t *testing.T) {
	input := []byte("services: {web: {image: 'nginx:stable', labels: ['team=ops'], environment: ['MODE=production']}}")
	artifact, err := CompileExisting(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"nginx:stable", `"MODE":"production"`, `"team":"ops"`} {
		if !strings.Contains(artifact.Compose(), expected) {
			t.Fatalf("missing %s in %s", expected, artifact.Compose())
		}
	}
	if _, err := CompileExisting(context.Background(), []byte(strings.ReplaceAll(string(input), "MODE=production", "MODE"))); err == nil {
		t.Fatal("inheriting the execution environment should be rejected")
	}
}
