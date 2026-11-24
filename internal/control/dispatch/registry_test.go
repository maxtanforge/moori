package dispatch_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/control/dispatch"
	"github.com/maxtanforge/moori/internal/control/storage"
	core "github.com/maxtanforge/moori/internal/core/registry"
	"github.com/maxtanforge/moori/internal/core/release"
	node "github.com/maxtanforge/moori/internal/node/registry"
)

func TestRegistryCredentialsRequireTrustedNodeTransport(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	admin, err := store.InitializeAdmin(ctx, "admin", "initial password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	agent, generation, err := store.IssueAgentCredential(ctx, "edge", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterAssignedTarget(ctx, "team", "prod", "edge", generation); err != nil {
		t.Fatal(err)
	}
	key := storage.SecretKey{13}
	const image = "registry.example/team/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	item, err := store.CreateHumanRegistry(ctx, admin, key, storage.RegistryInput{Name: "Shared", Host: "registry.example", Username: "user", Password: "test-only-password"})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := release.Compile(ctx, []byte("services: {web: {image: '"+image+"'}}"), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PublishRelease(ctx, "team", "r1", artifact); err != nil {
		t.Fatal(err)
	}
	ci, err := store.IssueServiceCredential(ctx, "ci", "team", "prod", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdmitPublishedRelease(ctx, ci, storage.Admission{ID: "task", Project: "team", Target: "prod", Key: "one", Release: "r1"}); err != nil {
		t.Fatal(err)
	}
	offer, err := store.NextOffer(ctx, "edge", generation, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	untrusted := http.NewServeMux()
	untrusted.Handle("POST /agent/v1/executions/{execution}/registry", dispatch.CredentialRegistryEndpoint(store, key))
	direct := httptest.NewServer(untrusted)
	defer direct.Close()
	endpoint := direct.URL + "/agent/v1/executions/" + offer.Execution + "/registry"
	req, err := http.NewRequest(http.MethodPost, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+agent)
	req.Header.Set("X-Moori-Snapshot-Digest", offer.Digest)
	req.Header.Set("X-Forwarded-Proto", "https")
	resp, err := direct.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUpgradeRequired {
		t.Fatalf("untrusted forwarded proto was accepted: %d", resp.StatusCode)
	}

	trusted := http.NewServeMux()
	trusted.Handle("POST /agent/v1/executions/{execution}/registry", dispatch.CredentialRegistryEndpoint(store, key, netip.MustParsePrefix("127.0.0.1/32")))
	origin := httptest.NewServer(trusted)
	defer origin.Close()
	originURL, _ := url.Parse(origin.URL)
	proxy := &httputil.ReverseProxy{Rewrite: func(req *httputil.ProxyRequest) {
		req.SetURL(originURL)
		req.SetXForwarded()
	}}
	gateway := httptest.NewTLSServer(proxy)
	defer gateway.Close()
	roots := x509.NewCertPool()
	roots.AddCert(gateway.Certificate())
	target, _ := url.Parse("wss" + strings.TrimPrefix(gateway.URL, "https"))
	client, err := node.NewCredentialClient(target, &tls.Config{RootCAs: roots}, agent)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	result, err := client.Fetch(ctx, offer.Execution, offer.Digest)
	if err != nil || result[image].ServerAddress != "registry.example" {
		t.Fatalf("trusted gateway did not release credential: %v", err)
	}
	if _, err := client.Fetch(ctx, offer.Execution, "sha256:"+strings.Repeat("0", 64)); !errors.Is(err, core.ErrDenied) {
		t.Fatalf("different digest was not denied: %v", err)
	}
	if err := store.SetHumanRegistryEnabled(ctx, admin, item.ID, false); err != nil {
		t.Fatal(err)
	}
	plainURL, _ := url.Parse("ws" + strings.TrimPrefix(direct.URL, "http"))
	plain, err := node.NewCredentialClient(plainURL, nil, agent)
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	anonymous, err := plain.Fetch(ctx, offer.Execution, offer.Digest)
	if err != nil || len(anonymous) != 0 {
		t.Fatalf("anonymous execution was rejected or got credentials: %v", err)
	}
}
