package release

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestTemplateBindingsVerifyAndRequireTargetNames(t *testing.T) {
	ctx := context.Background()
	input := `services:
  app:
    image: nginx@` + digest + `
    environment: {MODE: old, PASSWORD_FILE: /run/secrets/PASSWORD}
    ports: ["127.0.0.1:8000:80/tcp"]
    volumes: ["data:/data"]
    networks: [shared]
volumes:
  data: {name: old-data, external: true}
networks:
  shared: {name: old-network, external: true}
`
	base, fields, err := PrepareTemplate(ctx, []byte(input), TemplateParameters{
		Ports:       []TemplatePort{{Service: "app", Port: 0}},
		Environment: []TemplateEnvironment{{Service: "app", Key: "MODE"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(base.Compose(), "old-data") || strings.Contains(base.Compose(), "old-network") ||
		len(fields.Volumes) != 1 || !fields.Volumes[0].Required || len(fields.Networks) != 1 || !fields.Networks[0].Required {
		t.Fatalf("template retained node bindings: %s, %+v", base.Compose(), fields)
	}
	if _, err := InstantiateTemplate(ctx, base.Compose(), fields, TemplateOverrides{}); err == nil {
		t.Fatal("accepted missing bindings")
	}
	overrides := TemplateOverrides{
		Ports:       []TemplatePort{{Service: "app", Value: "9000"}},
		Environment: []TemplateEnvironment{{Service: "app", Key: "MODE", Value: "production"}},
		Volumes:     []TemplateResource{{Key: "data", Name: "node-data"}},
		Networks:    []TemplateResource{{Key: "shared", Name: "node-net"}},
	}
	bound, err := InstantiateTemplate(ctx, base.Compose(), fields, overrides)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := bound.Bind(Binding{Target: "target", ConfigVersion: "cfg", HealthTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	verified, err := Verify(ctx, snapshot.JSON(), snapshot.Digest())
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"127.0.0.1:9000:80/tcp", "production", "node-data", "node-net", "/run/secrets/PASSWORD"} {
		if !strings.Contains(verified.Compose, expected) {
			t.Errorf("expected %q in %s", expected, verified.Compose)
		}
	}
	overrides.Ports[0].Value = "9000:80"
	if _, err := InstantiateTemplate(ctx, base.Compose(), fields, overrides); err == nil {
		t.Fatal("accepted widening a loopback bind")
	}
	overrides.Ports = nil
	overrides.Networks[0].Name = "host"
	if _, err := InstantiateTemplate(ctx, base.Compose(), fields, overrides); err == nil {
		t.Fatal("accepted the host network")
	}
}

func TestTemplateRejectsInlineCredentialsAndUndeclaredFields(t *testing.T) {
	ctx := context.Background()
	base := `services: {app: {image: "nginx@` + digest + `", environment: {PASSWORD: secret, MODE: dev}}}`
	if _, _, err := PrepareTemplate(ctx, []byte(base), TemplateParameters{}); err == nil {
		t.Fatal("accepted a plaintext credential")
	}
	base = strings.Replace(base, "PASSWORD: secret, ", "", 1)
	template, fields, err := PrepareTemplate(ctx, []byte(base), TemplateParameters{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InstantiateTemplate(ctx, template.Compose(), fields, TemplateOverrides{
		Environment: []TemplateEnvironment{{Service: "app", Key: "MODE", Value: "prod"}},
	}); err == nil {
		t.Fatal("allowed modifying undeclared environment key")
	}
}
