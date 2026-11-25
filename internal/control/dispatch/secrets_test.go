package dispatch_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/control/dispatch"
	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/core/deployment"
	"github.com/maxtanforge/moori/internal/core/release"
	"github.com/maxtanforge/moori/internal/node/executor"
	"github.com/maxtanforge/moori/internal/node/ledger"
	"github.com/maxtanforge/moori/internal/node/secrets"
	"github.com/maxtanforge/moori/internal/node/session"
	"github.com/maxtanforge/moori/internal/platform/execlog"
	"github.com/maxtanforge/moori/internal/transport/agentwire"
)

func TestAgentCredentialGatewaySecretDelivery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dir := t.TempDir()
	control, err := storage.Open(ctx, filepath.Join(dir, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	agentToken, _, err := control.IssueAgentCredential(ctx, "agent", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.RegisterAssignedTarget(ctx, "project", "target", "agent", 1); err != nil {
		t.Fatal(err)
	}
	key := storage.SecretKey{42}
	const value = "test-only-versioned-secret"
	if err := control.PutSecret(ctx, key, "project", "target", "PASSWORD", "v1", []byte(value)); err != nil {
		t.Fatal(err)
	}
	if err := control.SetTargetSecrets(ctx, "project", "target", "cfg1", storage.SecretBindings{
		Secrets: map[string]string{"PASSWORD": "v1"}, Services: map[string][]string{"web": {"PASSWORD"}},
	}); err != nil {
		t.Fatal(err)
	}
	artifact, err := release.Compile(ctx, []byte("services: {web: {image: 'nginx@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'}}"), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.PublishRelease(ctx, "project", "r1", artifact); err != nil {
		t.Fatal(err)
	}
	token, err := control.IssueServiceCredential(ctx, "ci", "project", "target", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	id, err := control.AdmitPublishedRelease(ctx, token, storage.Admission{ID: "deploy", Project: "project", Target: "target", Release: "r1", Key: "pipeline"})
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /agent/v1/connect", dispatch.Endpoint(ctx, control, dispatch.EndpointOptions{}))
	mux.Handle("POST /agent/v1/executions/{execution}/secrets", dispatch.CredentialSecretEndpoint(control, key))
	backend := httptest.NewServer(mux)
	defer backend.Close()
	u, _ := url.Parse(backend.URL)
	srv := httptest.NewTLSServer(httputil.NewSingleHostReverseProxy(u))
	defer srv.Close()
	anonymous, err := srv.Client().Post(srv.URL+"/agent/v1/executions/deploy/secrets", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	anonymous.Body.Close()
	if anonymous.StatusCode != http.StatusUnauthorized {
		t.Fatal("anonymous secret fetch was accepted")
	}
	serverRoots := x509.NewCertPool()
	serverRoots.AddCert(srv.Certificate())
	tlsConfig := &tls.Config{RootCAs: serverRoots}
	endpoint, err := url.Parse("wss" + strings.TrimPrefix(srv.URL, "https") + "/agent/v1/connect")
	if err != nil {
		t.Fatal(err)
	}
	fetch, err := secrets.NewCredentialClient(endpoint, tlsConfig, agentToken)
	if err != nil {
		t.Fatal(err)
	}
	defer fetch.Close()
	book, err := ledger.Open(ctx, filepath.Join(dir, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer book.Close()
	logs, err := execlog.Open(filepath.Join(dir, "logs"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	staged := make(chan string, 1)
	runner := executor.Runner{Ledger: book, Logs: logs, FetchSecrets: fetch.Fetch,
		SecretRoot: filepath.Join(dir, "staged"), SecretBudget: 1 << 20, Apply: func(_ context.Context, p executor.Plan) error {
			if p.Output != nil || strings.Contains(p.Compose, value) {
				t.Error("secret exposed to Compose arguments or diagnostics")
				return errors.New("invalid execution plan")
			}
			var plan struct {
				Secrets map[string]struct {
					File string `json:"file"`
				} `json:"secrets"`
			}
			if err := json.Unmarshal([]byte(p.Compose), &plan); err != nil {
				return err
			}
			path := plan.Secrets["PASSWORD"].File
			if data, err := os.ReadFile(path); err != nil || string(data) != value {
				return errors.New("secret mount source unavailable")
			}
			staged <- path
			return nil
		},
	}

	{
		otherToken, _, e := control.IssueAgentCredential(ctx, "other-agent", time.Hour)
		if e != nil {
			t.Fatal(e)
		}
		other, e := secrets.NewCredentialClient(endpoint, tlsConfig, otherToken)
		if e != nil {
			t.Fatal(e)
		}
		defer other.Close()
		runner.FetchSecrets = func(ctx context.Context, execution, digest string) (map[string][]byte, error) {
			if _, e := other.Fetch(ctx, execution, digest); !errors.Is(e, secrets.ErrDenied) {
				t.Errorf("cross-agent secret access: %v", e)
				return nil, errors.New("cross-agent access accepted")
			}
			if _, e := fetch.Fetch(ctx, execution, "sha256:"+strings.Repeat("b", 64)); !errors.Is(e, secrets.ErrDenied) {
				t.Errorf("wrong snapshot accepted: %v", e)
				return nil, errors.New("wrong snapshot accepted")
			}
			return fetch.Fetch(ctx, execution, digest)
		}
	}
	client := session.Client{Session: session.Session{Ledger: book}, Execute: runner.RunAccepted, Interval: 20 * time.Millisecond,
		Dial: func(ctx context.Context) (*agentwire.Channel, error) {
			return agentwire.DialCredential(ctx, endpoint.String(), tlsConfig, agentToken)
		}}
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	defer func() { cancel(); <-done }()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("secret-enabled deployment did not complete")
		case <-tick.C:
			view, err := control.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if view.State != deployment.Succeeded {
				continue
			}
			entry, err := book.Get(ctx, view.Execution)
			if err != nil || strings.Contains(entry.Document, value) {
				t.Fatalf("secret was persisted to the ledger: %v", err)
			}
			select {
			case path := <-staged:
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("active mount was removed after success: %v", err)
				}
			default:
				t.Fatal("completed deployment did not stage a mount")
			}
			return
		}
	}
}
