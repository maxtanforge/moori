// moori-agent runs the new ledger-backed Agent on Linux.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/maxtanforge/moori/internal/node/execstream"
	"github.com/maxtanforge/moori/internal/node/executor"
	"github.com/maxtanforge/moori/internal/node/hostmetrics"
	"github.com/maxtanforge/moori/internal/node/identity"
	"github.com/maxtanforge/moori/internal/node/ledger"
	"github.com/maxtanforge/moori/internal/node/localinv"
	"github.com/maxtanforge/moori/internal/node/logstream"
	"github.com/maxtanforge/moori/internal/node/management"
	noderegistry "github.com/maxtanforge/moori/internal/node/registry"
	"github.com/maxtanforge/moori/internal/node/secrets"
	"github.com/maxtanforge/moori/internal/node/session"
	"github.com/maxtanforge/moori/internal/platform/execlog"
	"github.com/maxtanforge/moori/internal/platform/maintenance"
	"github.com/maxtanforge/moori/internal/platform/sqlite"
	"github.com/maxtanforge/moori/internal/transport/agentwire"
	"github.com/maxtanforge/moori/internal/transport/imagearchive"
)

func run(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "gc-secrets" {
		return gcSecrets(ctx, args[1:])
	}
	if len(args) > 0 && args[0] == "retire-execution" {
		return retireExecution(ctx, args[1:])
	}
	f := flag.NewFlagSet("moori-agent", flag.ContinueOnError)
	retention := f.Duration("ledger-retention", 30*24*time.Hour, "acknowledged terminal ledger retention; 0 disables pruning")
	endpoint := f.String("endpoint", os.Getenv("MOORI_ENDPOINT"), "WS or WSS Agent endpoint (defaults to MOORI_ENDPOINT)")
	credentialPath := f.String("credential-file", "", "private Agent bearer credential file")
	db := f.String("db", sqlite.DefaultPath("agent.db"), "Agent ledger")
	ca := f.String("control-ca", "", "PEM CA for the control plane")
	docker := f.String("docker", "docker", "trusted Docker executable")
	logDir := f.String("log-dir", "", "deployment output spool (default: <db>.logs)")
	logBudget := f.Int64("log-budget", 16<<20, "deployment output spool budget in bytes, or 0 to disable")
	metricsEnabled := f.Bool("host-metrics", true, "send current Linux host metrics")
	inventoryEnabled := f.Bool("inventory", true, "report local Docker resources and filesystem usage to the control plane")
	secretDir := f.String("secret-dir", "", "private directory for mounted deployment secrets (default: <db>.secrets)")
	secretBudget := f.Int64("secret-budget", 64<<20, "local mounted secret budget in bytes, or 0 to disable")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *retention < 0 {
		return errors.New("ledger-retention must be nonnegative")
	}
	u, err := url.Parse(*endpoint)

	if err != nil || u.Host == "" || u.User != nil || f.NArg() != 0 || (u.Scheme != "ws" && u.Scheme != "wss") {
		return errors.New("requires a WS or WSS -endpoint")
	}
	token := os.Getenv("MOORI_AGENT_TOKEN")
	if token != "" && *credentialPath != "" {
		return errors.New("choose either MOORI_AGENT_TOKEN or -credential-file")
	}
	var credential string
	if *credentialPath != "" {
		credentialLock, lockErr := lockAgentFile(*credentialPath)
		if lockErr != nil {
			return lockErr
		}
		defer credentialLock.Close()
		credential, err = identity.ReadCredential(*credentialPath)
	} else if token != "" {
		// Docker and Compose subprocesses do not need the control-plane token.
		_ = os.Unsetenv("MOORI_AGENT_TOKEN")
		credential, err = identity.ParseAgentToken(token)
	} else {
		return errors.New("requires MOORI_AGENT_TOKEN or -credential-file")
	}
	if err != nil {
		return err
	}
	dockerPath, err := exec.LookPath(*docker)
	if err != nil {
		return fmt.Errorf("the Docker CLI is required on agent nodes (-docker names the executable): %w", err)
	}
	roots, err := controlRoots(*ca)
	if err != nil {
		return err
	}
	lock, err := lockLedger(*db)
	if err != nil {
		return err
	}
	defer lock.Close()
	cfg := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	book, err := ledger.Open(ctx, *db)
	if err != nil {
		return err
	}
	defer book.Close()
	if *retention > 0 {
		defer maintenance.Start(ctx, "Agent ledger pruning failed", time.Hour, func(ctx context.Context) error { return book.Prune(ctx, time.Now().Add(-*retention)) })()
	}
	if *logDir == "" {
		*logDir = *db + ".logs"
	}
	var logs *execlog.Store
	if *logBudget != 0 {
		logs, err = execlog.Open(*logDir, *logBudget)
		if err != nil {
			slog.Warn("deployment output spool unavailable", "err", err)
		}
	}
	imageEngine, err := noderegistry.NewLocalEngine()
	if err != nil {
		return err
	}
	defer imageEngine.Close()
	registries, err := noderegistry.NewCredentialClient(u, cfg, credential)
	if err != nil {
		return err
	}
	defer registries.Close()
	operations, err := management.Open(imageEngine, registries.FetchOperation)
	if err != nil {
		return err
	}
	defer operations.Close()
	operations.EnableComposeSources(dockerPath)
	if u.Scheme == "wss" {
		archives, err := imagearchive.NewClient(u, cfg, credential)
		if err != nil {
			return err
		}
		defer archives.Close()
		operations.Archives = archives
	}
	runner := executor.Runner{Ledger: book, Apply: executor.LocalCompose(dockerPath), Logs: logs, PullImage: imageEngine.Pull, FetchRegistry: registries.Fetch}
	if *secretBudget > 0 {
		fetcher, err := secrets.NewCredentialClient(u, cfg, credential)
		if err != nil {
			return err
		}
		defer fetcher.Close()
		if *secretDir == "" {
			*secretDir = *db + ".secrets"
		}
		runner.FetchSecrets = fetcher.Fetch
		runner.SecretRoot = *secretDir
		runner.SecretBudget = *secretBudget
	}
	client := session.Client{
		Session: session.Session{Ledger: book}, Execute: runner.RunAccepted, Logs: logs, NodeOperation: operations.Run,
		Dial: func(ctx context.Context) (*agentwire.Channel, error) {
			return agentwire.DialCredential(ctx, u.String(), cfg, credential)
		},
	}
	if *metricsEnabled {
		client.Collect = hostmetrics.New(filepath.Dir(*db)).Sample
	}
	if *inventoryEnabled {
		docker, closeDocker, err := localinv.Dial()
		if err != nil {
			slog.Warn("local Docker inventory unavailable", "err", err)
		} else {
			defer closeDocker()
		}
		client.Inventory = localinv.New(docker).Overview
	}
	if u.Scheme == "wss" {
		streams, err := logstream.NewClient(u, cfg, credential)
		if err != nil {
			return err
		}
		defer streams.Close()
		streamCtx, stopStreams := context.WithCancel(ctx)
		streamsDone := make(chan struct{})
		go func() {
			defer close(streamsDone)
			if err := streams.Run(streamCtx, operations.FollowLogs); err != nil && streamCtx.Err() == nil {
				slog.Debug("Agent log streaming ended", "err", err)
			}
		}()
		defer func() {
			stopStreams()
			<-streamsDone
		}()

		// Container terminals ride the same verified WSS endpoint.
		docker, closeDocker, err := localinv.DialClient()
		if err != nil {
			slog.Warn("container terminals unavailable", "err", err)
		} else {
			defer closeDocker()
			terminals, err := execstream.NewClient(u, cfg, credential)
			if err != nil {
				return err
			}
			termCtx, stopTerminals := context.WithCancel(ctx)
			terminalsDone := make(chan struct{})
			go func() {
				defer close(terminalsDone)
				if err := terminals.Run(termCtx, execstream.DockerStarter(docker)); err != nil && termCtx.Err() == nil {
					slog.Debug("Agent terminal streaming ended", "err", err)
				}
			}()
			defer func() {
				stopTerminals()
				<-terminalsDone
			}()
		}
	}
	return client.Run(ctx)
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, flag.ErrHelp) {
		slog.Error("Agent command failed", "err", err)
		os.Exit(1)
	}
}
