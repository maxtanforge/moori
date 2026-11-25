package release

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestVolumesAndHealthcheckSurviveAgentVerification(t *testing.T) {
	input := `services:
  db:
    image: postgres@` + digest + `
    command: [postgres]
    environment: {MODE: production}
    volumes: ["data:/var/lib/postgresql/data", "cache:/cache:ro"]
    healthcheck:
      test: [CMD-SHELL, "pg_isready -U postgres"]
      interval: 10s
      timeout: 2s
      retries: 5
      start_period: 30s
      start_interval: 1s
volumes: {data: {}, cache: {}}
`
	r, err := Compile(context.Background(), []byte(input), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := r.Bind(Binding{Target: "db", ConfigVersion: "1", HealthTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	v, err := Verify(context.Background(), s.JSON(), s.Digest())
	if err != nil {
		t.Fatal(err)
	}
	if v.Compose != r.Compose() {
		t.Fatal("agent changed Compose")
	}
	for _, want := range []string{`"volumes":{"cache":{},"data":{}}`, `"volumes":["data:/var/lib/postgresql/data","cache:/cache:ro"]`, `"start_period":"30s"`, `"retries":5`, `"command":["postgres"]`, `"MODE":"production"`} {
		if !strings.Contains(v.Compose, want) {
			t.Errorf("lost %s: %s", want, v.Compose)
		}
	}
}

func TestRejectUnsafeVolumesAndMalformedHealthchecksAtBothBoundaries(t *testing.T) {
	for _, fragment := range []string{
		`"volumes":["/:/host"]`, `"volumes":["./data:/data"]`, `"volumes":["data:/data"]`,
		`"volumes":["data:/data:ro,z"]`, `"volumes":["data:/a/../b"]`, `"volumes":["data:relative"]`,
		`"volumes":["data:/"]`, `"volumes":["data:/data","data:/data:ro"]`,
		`"volumes":[{"type":"bind","source":"/","target":"/host"}]`,
		`"healthcheck":{"test":["NONE"]}`, `"healthcheck":{"test":[]}`, `"healthcheck":{"test":["CMD"]}`,
		`"healthcheck":{"test":["CMD-SHELL","true","extra"]}`, `"healthcheck":{"test":["CMD","true"],"interval":"-1s"}`,
		`"healthcheck":{"test":["CMD","true"],"timeout":"31m"}`, `"healthcheck":{"test":["CMD","true"],"retries":0}`,
		`"healthcheck":{"test":["CMD","true"],"retries":101}`, `"healthcheck":{"test":["CMD","true"],"start_period":"bad"}`,
		`"healthcheck":{"test":["CMD","true"],"start_interval":"0s"}`, `"healthcheck":{"test":["CMD","${HOST}"]}`,
		`"healthcheck":{"test":["CMD","true"],"disable":true}`,
		`"healthcheck":{"test":["CMD","true"],"interval":""}`,
	} {
		t.Run(fragment, func(t *testing.T) {
			input := `{"services":{"db":{"image":"postgres@` + digest + `",` + fragment + `}}}`
			if !strings.Contains(fragment, `"volumes":["data:/data"]`) {
				input = strings.TrimSuffix(input, "}") + `,"volumes":{"data":{}}}`
			}
			assertRejectedAtBothBoundaries(t, input)
		})
	}
	for _, definition := range []string{`{"external":true}`, `{"name":"other-project"}`, `{"driver":"local"}`, `{"driver_opts":{"device":"/","type":"none","o":"bind"}}`} {
		assertRejectedAtBothBoundaries(t, `{"services":{"db":{"image":"postgres@`+digest+`"}},"volumes":{"data":`+definition+`}}`)
	}
}

func assertRejectedAtBothBoundaries(t *testing.T, input string) {
	t.Helper()
	if _, err := Compile(context.Background(), []byte(input), nil, nil, nil); err == nil {
		t.Errorf("compiler accepted %s", input)
	}
	wire, err := json.Marshal(snapshotDocument{Schema: 1, Target: "db", ConfigVersion: "1", HealthTimeoutNS: int64(time.Minute), ReleaseDigest: hash([]byte(input)), Compose: json.RawMessage(input)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(context.Background(), string(wire), hash(wire)); err == nil {
		t.Errorf("agent accepted %s", input)
	}
}

func TestSecretBindingsRejectOverlappingVolumeTargets(t *testing.T) {
	for _, target := range []string{"/run", "/run/secrets", "/run/secrets/TOKEN", "/run/secrets/TOKEN/child"} {
		r, err := Compile(context.Background(), []byte(`{"services":{"db":{"image":"postgres@`+digest+`","volumes":["data:`+target+`"]}},"volumes":{"data":{}}}`), nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.Bind(Binding{Target: "db", ConfigVersion: "1", HealthTimeout: time.Minute, Secrets: map[string]string{"TOKEN": "v1"}, SecretServices: map[string][]string{"db": {"TOKEN"}}}); err == nil {
			t.Errorf("accepted secret overlap at %s", target)
		}
	}
}
