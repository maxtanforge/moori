// moori serves HTTP behind a gateway, with optional direct TLS.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/maxtanforge/moori/internal/control/ci"
	"github.com/maxtanforge/moori/internal/control/dispatch"
	"github.com/maxtanforge/moori/internal/control/human"
	"github.com/maxtanforge/moori/internal/control/integration/gitea"
	"github.com/maxtanforge/moori/internal/control/localexec"
	"github.com/maxtanforge/moori/internal/control/observability"
	"github.com/maxtanforge/moori/internal/control/storage"
	controlweb "github.com/maxtanforge/moori/internal/control/web"
	"github.com/maxtanforge/moori/internal/platform/execlog"
	"github.com/maxtanforge/moori/internal/platform/maintenance"
	"github.com/maxtanforge/moori/internal/platform/sqlite"
	"github.com/maxtanforge/moori/internal/transport/execstream"
	"github.com/maxtanforge/moori/internal/transport/imagearchive"
	"github.com/maxtanforge/moori/internal/transport/logstream"
)

func defaultDB() string { return sqlite.DefaultPath("moori.db") }

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New(controlUsage)
	}
	switch args[0] {
	case "issue-agent-credential", "revoke-agent-credential":
		return agentCredentialCommand(ctx, args[0], args[1:], os.Stdout, os.Stderr)
	case "serve":
		return serve(ctx, args[1:])
	case "retire-local-execution":
		return retireLocalExecution(ctx, args[1:])
	case "backup-db", "verify-backup", "prepare-restore", "restore-status", "restore-inventory", "restore-release-target":
		return backupCommand(ctx, args[0], args[1:])
	case "agent-health":
		return agentHealth(ctx, args[1:])
	case "init-secret-key":
		return initSecretKey(args[1:])
	case "put-secret", "revoke-secret", "set-target-secrets":
		return localCommand(ctx, args[0], args[1:])
	case "abandon-deployment", "assign-target", "register-target", "register-container-target", "publish-release", "publish-template", "issue-ci-token", "revoke-ci-token", "create-user", "grant-platform-admin", "revoke-platform-admin", "cancel-deployment", "rollback-deployment":
		return localCommand(ctx, args[0], args[1:])
	default:
		return errors.New(controlUsage)
	}
}

func serve(ctx context.Context, args []string) error {
	f := flag.NewFlagSet("serve", flag.ContinueOnError)
	retention := f.Duration("outbox-retention", 30*24*time.Hour, "undelivered notification and deployment audit retention; 0 disables pruning")
	templateVersions := f.Int("template-version-retention", 0, "keep the newest N versions per console template; 0 keeps all versions")
	addr := f.String("addr", "0.0.0.0:8080", "HTTP address (HTTPS when server TLS is configured)")
	db := f.String("db", defaultDB(), "SQLite database")
	cert := f.String("tls-cert", "", "PEM server certificate")
	key := f.String("tls-key", "", "PEM server private key")
	hooks := f.String("gitea-hooks", "", "local JSON file containing Gitea webhook bindings")
	logDir := f.String("log-dir", "", "deployment output directory (default: <db>.logs)")
	logBudget := f.Int64("log-budget", 64<<20, "deployment output budget in bytes, or 0 to disable")
	metricsPath := f.String("metrics-db", "", "metrics SQLite path (default: <db>.metrics)")
	metricsEnabled := f.Bool("host-metrics", true, "retain current contact and resource metrics")
	secretPath := f.String("secret-key", os.Getenv("MOORI_SECRET_KEY_FILE"), "control secret key file (or MOORI_SECRET_KEY_FILE); defaults to a file next to the database")
	localMode := f.String("local-execution", "auto", "built-in local Docker execution: auto, on or off")
	localDB := f.String("local-db", "", "built-in execution ledger (default: <db>.local.db)")
	localDocker := f.String("local-docker", "docker", "trusted local Docker executable")
	localSecretDir := f.String("local-secret-dir", "", "local mounted secret directory (same absolute path on host and container)")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *retention < 0 {
		return errors.New("outbox-retention must be nonnegative")
	}
	if *templateVersions < 0 {
		return errors.New("template-version-retention must be nonnegative")
	}
	if *logBudget < 0 {
		return errors.New("log-budget must be nonnegative")
	}
	if f.NArg() != 0 {
		return errors.New("unexpected serve arguments")
	}
	tlsConfig, err := loadTransportSecurity(*cert, *key)
	if err != nil {
		return err
	}
	var monitoring monitoringConfig
	if *metricsEnabled {
		monitoring, err = configureMonitoring()
		if err != nil {
			return err
		}
	}
	store, err := storage.Open(ctx, *db)
	if err != nil {
		return err
	}
	defer store.Close()
	nodeState, err := observability.OpenNodeState(ctx, *db+".resources")
	if err != nil {
		return fmt.Errorf("open node resource store: %w", err)
	}
	defer nodeState.Close()
	if *logDir == "" {
		*logDir = *db + ".logs"
	}
	var logs *execlog.Store
	if *logBudget != 0 {
		logs, err = execlog.Open(*logDir, *logBudget)
		if err != nil {
			slog.Warn("deployment output storage unavailable", "err", err)
		}
	}
	var health *observability.Store
	if *metricsEnabled {
		if *metricsPath == "" {
			*metricsPath = *db + ".metrics"
		}
		health, err = observability.Open(ctx, *metricsPath)
		if err != nil {
			slog.Warn("host metrics storage unavailable", "err", err)
		} else {
			defer health.Close()
			// Upgrade path: keep the environment webhook delivering until the
			// console manages notification targets of its own.
			if _, err := store.EnsureAlertTarget(ctx, os.Getenv("MOORI_ALERT_WEBHOOK")); err != nil {
				slog.Warn("could not import MOORI_ALERT_WEBHOOK; manage notification targets in the console", "err", err)
			}
		}
	}
	mux := http.NewServeMux()
	controlweb.Mount(mux)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := store.CheckDatabase(ctx); err != nil {
			http.Error(w, "control database unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	archives := new(imagearchive.Relay)
	mux.Handle("GET /agent/v1/connect", dispatch.Endpoint(ctx, store, dispatch.EndpointOptions{Logs: logs, Health: health, NodeState: nodeState, Authenticate: dispatch.AuthenticateCredential(store), AbortArchive: archives.Abort}))
	automaticSecretKey := *secretPath == ""
	if automaticSecretKey {
		*secretPath = defaultSecretKeyPath(*db)
	}
	keyForSecrets, err := controlSecretKey(ctx, store, *secretPath, automaticSecretKey)
	if err != nil {
		return err
	}
	if keyForSecrets == (storage.SecretKey{}) {
		slog.Warn("control secret key missing for existing vault; restore the original key and restart", "path", *secretPath)
	}
	mux.Handle("POST /agent/v1/executions/{execution}/secrets", dispatch.CredentialSecretEndpoint(store, keyForSecrets))
	trustedProxies, err := configuredTrustedProxies(os.Getenv("MOORI_TRUSTED_PROXY_CIDRS"))
	if err != nil {
		return err
	}
	mux.Handle("POST /agent/v1/executions/{execution}/registry", dispatch.CredentialRegistryEndpoint(store, keyForSecrets, trustedProxies...))
	mux.Handle("POST /agent/v1/operations/{operation}/registry", dispatch.CredentialRegistryOperationEndpoint(store, keyForSecrets, trustedProxies...))
	human.MountRegistries(mux, store, keyForSecrets, trustedProxies...)
	archiveEndpoint := dispatch.ImageArchiveEndpoint(store, archives, trustedProxies...)
	mux.Handle("GET /agent/v1/operations/{operation}/image-archive", archiveEndpoint)
	mux.Handle("POST /agent/v1/operations/{operation}/image-archive", archiveEndpoint)
	human.MountImageArchives(mux, store, archives, trustedProxies...)
	execBroker := new(execstream.Broker)
	mux.Handle("GET /agent/v1/exec-stream", dispatch.ExecStreamEndpoint(store, execBroker, trustedProxies...))
	liveLogs := new(logstream.Broker)
	logEndpoint := dispatch.LiveLogEndpoint(store, liveLogs, trustedProxies...)
	mux.Handle("GET /agent/v1/log-stream/next", logEndpoint)
	mux.Handle("POST /agent/v1/log-stream/{ticket}", logEndpoint)
	mux.Handle("POST /api/v1/deployments", ci.Handler{Store: store})
	ci.MountContainerWebhooks(mux, store)
	ci.MountContainerDeployments(mux, store)
	mux.Handle("GET /api/v1/deployments/{id}", ci.Handler{Store: store})
	mux.Handle("POST /api/v1/deployments/{id}/cancel", ci.Handler{Store: store})
	mux.HandleFunc("POST /api/v1/deployments/{id}/rollback", ci.Handler{Store: store}.Rollback)
	mux.HandleFunc("POST /api/v1/releases", ci.Handler{Store: store}.Publish)
	mux.HandleFunc("GET /api/v1/projects/{project}/targets/{target}/releases/{release}", ci.Handler{Store: store}.ReadRelease)
	mux.HandleFunc("GET /api/v1/deployments/{id}/logs", ci.Handler{Store: store, Logs: logs}.Output)
	if *hooks != "" {
		bindings, err := loadGiteaBindings(*hooks)
		if err != nil {
			return err
		}
		hook, err := gitea.New(store, bindings)
		if err != nil {
			return err
		}
		mux.Handle("POST /api/v1/webhooks/gitea/{hook}", hook)
	}
	// Validate configuration and bind HTTP before starting local Docker work.
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	defer listener.Close()
	if *localDB == "" {
		*localDB = *db + ".local.db"
	}
	local, err := startLocalExecution(ctx, *localMode, *localDB, *localDocker, *localSecretDir, localexec.Options{Store: store, Logs: logs, Health: health, NodeState: nodeState, SecretKey: keyForSecrets, ImageArchives: archives})
	if err != nil {
		return err
	}
	defer local.close()
	if *retention > 0 || *templateVersions > 0 {
		defer maintenance.Start(ctx, "history pruning failed", time.Hour, func(ctx context.Context) error {
			if *retention > 0 {
				cutoff := time.Now().Add(-*retention)
				if err := store.PruneOutbox(ctx, cutoff); err != nil {
					return err
				}
				if err := store.PruneAudit(ctx, cutoff); err != nil {
					return err
				}
			}
			if *templateVersions > 0 {
				return store.PruneTemplateVersions(ctx, *templateVersions)
			}
			return nil
		})()
	}
	if health != nil {
		stopMonitoring := monitoring.start(ctx, store, health, local.enabled)
		defer stopMonitoring()
	}
	human.Mount(mux, store, human.ViewOptions{Logs: logs, Health: health, NodeState: nodeState, LocalExecution: local.enabled, LocalMessage: local.message, RunLocalOperation: local.runOperation, TrustedProxies: trustedProxies})
	human.MountLiveLogs(mux, store, liveLogs, local.followLogs, trustedProxies...)
	human.MountContainerTerminal(mux, store, execBroker, local.startTerminal, trustedProxies...)
	srv := &http.Server{
		Addr: *addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, MaxHeaderBytes: 16 << 10,
		TLSConfig: tlsConfig,
	}
	ended := make(chan error, 1)
	go func() {
		if tlsConfig != nil {
			ended <- srv.ServeTLS(listener, "", "")
		} else {
			ended <- srv.Serve(listener)
		}
	}()
	select {
	case err := <-local.done:
		_ = srv.Close()
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("built-in executor stopped: %w", err)
	case err := <-ended:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil {
			return errors.Join(err, srv.Close())
		}
		return nil
	}
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil && !errors.Is(err, flag.ErrHelp) {
		slog.Error("control command failed", "err", err)
		os.Exit(1)
	}
}

func configuredTrustedProxies(raw string) ([]netip.Prefix, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var proxies []netip.Prefix
	for _, value := range strings.Split(raw, ",") {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
		if err != nil || prefix.Bits() == 0 {
			return nil, errors.New("MOORI_TRUSTED_PROXY_CIDRS requires specific gateway addresses in CIDR notation")
		}
		proxies = append(proxies, prefix.Masked())
	}
	return proxies, nil
}
