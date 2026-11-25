package release

import (
	"strings"
	"testing"
)

func TestAdoptPreservesExternalResourceNamesAndProvidesPortOverrides(t *testing.T) {
	input := `services:
  app:
    image: busybox@sha256:` + strings.Repeat("a", 64) + `
    volumes: ["data:/app", "cache:/cache"]
    networks: [edge]
    ports: ["8080:80", "443"]
volumes:
  data: {external: true}
  cache: {external: false}
networks:
  edge: {external: true}
`
	adopted, err := Adopt(t.Context(), AdoptInput{Project: "old", Compose: []byte(input),
		Pins: map[string][]string{"app": {"busybox@sha256:" + strings.Repeat("a", 64)}}})
	if err != nil || adopted.Compose == "" {
		t.Fatalf("adopt: %+v, %v", adopted, err)
	}
	if strings.Join(adopted.Volumes, ",") != "data,old_cache" || strings.Join(adopted.Networks, ",") != "edge" {
		t.Fatalf("wrong runtime resource names: %v %v", adopted.Volumes, adopted.Networks)
	}
	if adopted.VolumeBindings["data"] != "data" || adopted.VolumeBindings["cache"] != "old_cache" ||
		adopted.NetworkBindings["edge"] != "edge" {
		t.Fatalf("wrong suggested bindings: %v %v", adopted.VolumeBindings, adopted.NetworkBindings)
	}
	if len(adopted.Ports) != 1 || adopted.Ports[0] != (TemplatePort{Service: "app", Port: 0, Value: "8080:80"}) {
		t.Fatalf("unexpected fixed ports: %+v", adopted.Ports)
	}
	base, fields, err := PrepareTemplate(t.Context(), []byte(adopted.Compose), TemplateParameters{
		Ports: []TemplatePort{{Service: "app", Port: 0}},
	})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := InstantiateTemplate(t.Context(), base.Compose(), fields, TemplateOverrides{
		Ports:    []TemplatePort{{Service: "app", Port: 0, Value: "18080:80"}},
		Volumes:  []TemplateResource{{Key: "data", Name: "data"}, {Key: "cache", Name: "old_cache"}},
		Networks: []TemplateResource{{Key: "edge", Name: "edge"}},
	})
	if err != nil || !strings.Contains(bound.Compose(), "18080:80") {
		t.Fatalf("temporary port override could not instantiate the adopted template: %v", err)
	}
}

func TestAdoptConvertsRepresentableProject(t *testing.T) {
	input := `services:
  web:
    container_name: myweb
    image: nginx:1.27
    restart: unless-stopped
    depends_on: [api]
    ports:
      - "127.0.0.1:8080:80"
      - {target: 53, published: 1053, protocol: udp}
      - "5353:5353/udp"
    volumes:
      - "site:/usr/share/nginx/html:ro"
      - {type: volume, source: cache, target: /var/cache, read_only: false}
    environment:
      MODE: production
      TZ: Asia/Shanghai
    labels:
      team: platform
      com.docker.compose.project: myapp
    healthcheck:
      test: curl -fs http://localhost/ || exit 1
      interval: 30s
      retries: 3
  api:
    image: ghcr.io/example/api:2.1.0
    command: ["serve", "--port", "9000"]
    networks: [front, back]
volumes:
  site: {}
  cache:
networks:
  front: {}
  back:
`
	pins := map[string][]string{
		"web": {"nginx@sha256:" + strings.Repeat("b", 64)},
		"api": {"ghcr.io/example/api@sha256:" + strings.Repeat("c", 64)},
	}
	adopted, err := Adopt(t.Context(), AdoptInput{Project: "myapp", Compose: []byte(input), Pins: pins})
	if err != nil {
		t.Fatal(err)
	}
	if adopted.Compose == "" {
		t.Fatalf("expected a converted document, issues %+v", adopted.Issues)
	}
	if len(adopted.Services) != 2 || len(adopted.Issues) != 3 {
		t.Fatalf("services %v issues %+v", adopted.Services, adopted.Issues)
	}
	for _, expected := range []string{
		"image: nginx@sha256:" + strings.Repeat("b", 64),
		"image: ghcr.io/example/api@sha256:" + strings.Repeat("c", 64),
		"127.0.0.1:8080:80", "1053:53/udp", "5353:5353/udp",
		"site:/usr/share/nginx/html:ro", "cache:/var/cache",
		"serve", "--port", "9000",
		"curl -fs http://localhost/ || exit 1", "interval: 30s", "retries: 3",
		"restart: unless-stopped", "MODE: production", "TZ: Asia/Shanghai", "team: platform",
	} {
		if !strings.Contains(adopted.Compose, expected) {
			t.Errorf("expected %q in %s", expected, adopted.Compose)
		}
	}
	for _, leaked := range []string{"container_name", "depends_on", "com.docker.compose"} {
		if strings.Contains(adopted.Compose, leaked) {
			t.Errorf("dropped setting %q leaked into %s", leaked, adopted.Compose)
		}
	}
	if len(adopted.Volumes) != 2 || len(adopted.Networks) != 3 {
		t.Fatalf("resources %v %v", adopted.Volumes, adopted.Networks)
	}
	for _, expected := range []string{"myapp_site", "myapp_cache", "myapp_front", "myapp_back", "myapp_default"} {
		if !strings.Contains(adopted.Compose, "name: "+expected) {
			t.Errorf("expected external resource %q in %s", expected, adopted.Compose)
		}
	}
	// The converted document must be accepted by the template flow as-is.
	if _, _, err := PrepareTemplate(t.Context(), []byte(adopted.Compose), TemplateParameters{}); err != nil {
		t.Fatalf("PrepareTemplate rejected converted document: %v", err)
	}
}

func TestAdoptReportsBlockersAndKeepsConvertibleServices(t *testing.T) {
	input := `services:
  builder:
    build: ./app
    command: echo hi
  assets:
    image: node:22
    volumes: ["./static:/out"]
  tracker:
    image: nginx:1.27
    env_file: secrets.env
  db:
    image: postgres:17
    environment:
      POSTGRES_PASSWORD: "${DB_PASSWORD}"
      POSTGRES_DB: null
    deploy:
      resources:
        limits: {memory: 512M}
  api:
    image: ghcr.io/example/api@sha256:` + strings.Repeat("d", 64) + `
    ports:
      - {target: 80, published: 8080, mode: host}
      - "9000-9010:9000"
    healthcheck: {disable: true}
volumes:
  data: {}
`
	adopted, err := Adopt(t.Context(), AdoptInput{Project: "legacy", Compose: []byte(input), Pins: map[string][]string{
		"api":    {"ghcr.io/example/api@sha256:" + strings.Repeat("d", 64)},
		"assets": {"node@sha256:" + strings.Repeat("e", 64)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(adopted.Services) != 1 || adopted.Services[0] != "api" || adopted.Compose == "" {
		t.Fatalf("expected only api to convert, got %v issues %+v", adopted.Services, adopted.Issues)
	}
	if strings.Contains(adopted.Compose, "ports:") || strings.Contains(adopted.Compose, "healthcheck") {
		t.Errorf("dropped ports or healthcheck leaked into %s", adopted.Compose)
	}
	messages := map[string][]string{}
	for _, issue := range adopted.Issues {
		messages[issue.Service] = append(messages[issue.Service], issue.Message)
	}
	for service, fragment := range map[string]string{
		"builder": "build", "assets": "绑定挂载", "tracker": "env_file", "db": "deploy", "api": "host 模式",
	} {
		if !containsFragment(messages[service], fragment) {
			t.Errorf("service %s lacks a %q issue: %v", service, fragment, messages[service])
		}
	}
	for _, fragment := range []string{"端口", "disable"} {
		if !containsFragment(messages["api"], fragment) {
			t.Errorf("api issues miss %q: %v", fragment, messages["api"])
		}
	}
}

func TestAdoptOmitsServicesWithoutMatchingDigests(t *testing.T) {
	input := "services:\n  web:\n    image: nginx:1.27\n  other:\n    image: redis:7\n"
	adopted, err := Adopt(t.Context(), AdoptInput{Project: "app", Compose: []byte(input),
		Pins: map[string][]string{"web": {"registry.example/library/nginx@sha256:" + strings.Repeat("a", 64)}}})
	if err != nil {
		t.Fatal(err)
	}
	if adopted.Compose != "" {
		t.Fatalf("expected nothing convertible, got %s", adopted.Compose)
	}
	if len(adopted.Issues) != 2 || !containsFragment(issueMessages(adopted, "web"), "不匹配") ||
		!containsFragment(issueMessages(adopted, "other"), "没有") {
		t.Fatalf("issues %+v", adopted.Issues)
	}
}

func TestAdoptRejectsUnusableInput(t *testing.T) {
	digest64 := "sha256:" + strings.Repeat("a", 64)
	cases := map[string]AdoptInput{
		"项目名无效":  {Project: "bad project!", Compose: []byte("services: {}")},
		"输入为空":   {Project: "app"},
		"输入超限":   {Project: "app", Compose: []byte(strings.Repeat("# pad\n", 64<<10) + "services: {}")},
		"多文件":    {Project: "app", Compose: []byte("include:\n  - other.yaml\nservices: {web: {image: nginx}}")},
		"YAML无效": {Project: "app", Compose: []byte("services: [")},
		"多文档":    {Project: "app", Compose: []byte("services: {web: {image: nginx}}\n---\nservices: {}")},
		"缺少服务":   {Project: "app", Compose: []byte("volumes: {}")},
		"服务不是映射": {Project: "app", Compose: []byte("services: [web]")},
	}
	for name, input := range cases {
		if _, err := Adopt(t.Context(), input); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	// An unrepresentable setting found late is an issue, not a hard error.
	adopted, err := Adopt(t.Context(), AdoptInput{Project: "x",
		Compose: []byte("services:\n  web:\n    image: nginx\n    restart: sometimes"),
		Pins:    map[string][]string{"web": {"nginx@" + digest64}}})
	if err != nil || adopted.Compose != "" || !containsFragment(issueMessages(adopted, "web"), "restart") {
		t.Fatalf("invalid restart must become an issue: %q %+v %v", adopted.Compose, adopted.Issues, err)
	}
}

func TestAdoptCarriesShellCommandAndScalarHealthcheck(t *testing.T) {
	input := `services:
  app:
    image: busybox:1.37
    command: sh -c 'echo "hello world" && exec sleep 3600'
    healthcheck:
      test: ["CMD", "true"]
      start_period: 5s
  broken:
    image: busybox:1.37
    command: echo "unterminated
`
	pin := "busybox@sha256:" + strings.Repeat("a", 64)
	pins := map[string][]string{"app": {pin}, "broken": {pin}}
	adopted, err := Adopt(t.Context(), AdoptInput{Project: "shell", Compose: []byte(input), Pins: pins})
	if err != nil {
		t.Fatal(err)
	}
	if len(adopted.Services) != 1 || !strings.Contains(adopted.Compose, `echo "hello world" && exec sleep 3600`) {
		t.Fatalf("command not carried over: %q issues %+v", adopted.Compose, adopted.Issues)
	}
	if !strings.Contains(adopted.Compose, "start_period: 5s") || !strings.Contains(adopted.Compose, "CMD") {
		t.Errorf("healthcheck fields lost: %s", adopted.Compose)
	}
	if !containsFragment(issueMessages(adopted, "broken"), "command") {
		t.Errorf("broken command not reported: %+v", adopted.Issues)
	}
}

func issueMessages(adopted Adopted, service string) []string {
	var out []string
	for _, issue := range adopted.Issues {
		if issue.Service == service {
			out = append(out, issue.Message)
		}
	}
	return out
}

func containsFragment(values []string, fragment string) bool {
	for _, value := range values {
		if strings.Contains(value, fragment) {
			return true
		}
	}
	return false
}
