package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/maxtanforge/moori/internal/control/localexec"
	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/core/nodeops"
	"github.com/maxtanforge/moori/internal/core/registry"
	"github.com/maxtanforge/moori/internal/node/executor"
	"github.com/maxtanforge/moori/internal/node/hostmetrics"
	"github.com/maxtanforge/moori/internal/node/ledger"
	"github.com/maxtanforge/moori/internal/node/localinv"
	"github.com/maxtanforge/moori/internal/node/management"
	noderegistry "github.com/maxtanforge/moori/internal/node/registry"
	"github.com/maxtanforge/moori/internal/platform/execlog"
	"github.com/maxtanforge/moori/internal/platform/maintenance"
)

func startLocalExecution(ctx context.Context, mode, db, docker, secretRoot string, o localexec.Options) (localRuntime, error) {
	inactive := localRuntime{message: "本机 Docker 管理已通过 -local-execution=off 关闭。若需启用，请修改主服务启动参数。", close: func() {}}
	if mode == "off" {
		return inactive, nil
	}
	if mode != "on" && mode != "auto" {
		return inactive, errors.New("local-execution must be auto, on or off")
	}
	unavailable := func(err error) (localRuntime, error) {
		if mode == "on" {
			return inactive, fmt.Errorf("local Docker execution unavailable: %w", err)
		}
		slog.Warn("local Docker execution unavailable", "err", err)
		inactive.message = localDockerMessage(err, os.Geteuid())
		return inactive, nil
	}
	dockerPath, err := exec.LookPath(docker)
	if err != nil {
		return unavailable(&localDockerError{stage: "docker executable", cause: err})
	}
	connection, err := net.DialTimeout("unix", "/var/run/docker.sock", 2*time.Second)
	if err != nil {
		return unavailable(&localDockerError{stage: "docker socket", cause: err})
	}
	connection.Close()
	if err := probeLocalCompose(ctx, dockerPath); err != nil {
		return unavailable(err)
	}
	path, lock, err := lockLocalLedger(db)
	if err != nil {
		return inactive, err
	}
	exists, err := o.Store.HasLocalExecutor(ctx)
	if err != nil {
		lock.Close()
		return inactive, err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) && exists {
		lock.Close()
		return inactive, errors.New("local executor ledger is missing; preserve the original ledger and select it with -local-db")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		lock.Close()
		return inactive, err
	}
	book, err := ledger.Open(ctx, path)
	if err != nil {
		lock.Close()
		return inactive, err
	}
	if err := o.Store.EnsureLocalExecutor(ctx); err != nil {
		book.Close()
		lock.Close()
		return inactive, err
	}
	imageEngine, err := noderegistry.NewLocalEngine()
	if err != nil {
		book.Close()
		lock.Close()
		return inactive, err
	}
	operations, err := management.Open(imageEngine, func(ctx context.Context, id string) (registry.Credential, error) {
		return o.Store.RegistryOperation(ctx, o.SecretKey, storage.LocalExecutorID, 1, id, true)
	})
	if err != nil {
		imageEngine.Close()
		book.Close()
		lock.Close()
		return inactive, err
	}
	operations.Archives = o.ImageArchives
	operations.EnableComposeSources(dockerPath)
	o.NodeOperation = operations.Run
	o.PullImage = imageEngine.Pull
	o.Ledger = book
	o.Apply = executor.LocalCompose(dockerPath)
	o.Spool, err = execlog.Open(path+".logs", 16<<20)
	if err != nil {
		slog.Warn("local output spool unavailable", "err", err)
	}
	if secretRoot == "" {
		secretRoot = path + ".secrets"
	}
	o.SecretRoot = secretRoot
	if o.Health != nil {
		o.Collect = hostmetrics.New(filepath.Dir(path)).Sample
	}
	closeDocker := func() {}
	if o.Health != nil || o.NodeState != nil {
		docker, closeClient, err := localinv.Dial()
		if err != nil {
			slog.Warn("local Docker inventory unavailable", "err", err)
		} else {
			closeDocker = closeClient
		}
		o.Inventory = localinv.New(docker).Refresh
	}
	runCtx, cancel := context.WithCancel(ctx)
	prune := maintenance.Start(runCtx, "local ledger pruning failed", time.Hour, func(ctx context.Context) error { return book.Prune(ctx, time.Now().Add(-30*24*time.Hour)) })
	finished := make(chan struct{})
	done := make(chan error, 1)
	client := localexec.NewClient(o)
	refresh := make(chan struct{}, 1)
	client.InventoryRefresh = refresh
	runOperation := func(ctx context.Context, req nodeops.Request) (nodeops.Result, error) {
		result, err := operations.Run(ctx, req)
		if req.Action.Mutates() {
			select {
			case refresh <- struct{}{}:
			default:
			}
		}
		return result, err
	}
	go func() { defer close(finished); done <- client.Run(runCtx) }()
	slog.Info("built-in Docker executor started", "ledger", path)
	return localRuntime{enabled: true, done: done, followLogs: operations.FollowLogs, startTerminal: operations.TerminalStarter(), runOperation: runOperation, close: func() {
		cancel()
		<-finished
		closeDocker()
		operations.Close()
		imageEngine.Close()
		prune()
		book.Close()
		lock.Close()
	}}, nil
}

func lockLocalLedger(path string) (string, *os.File, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", nil, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return "", nil, err
	}
	absolute = filepath.Join(parent, filepath.Base(absolute))
	if canonical, e := filepath.EvalSymlinks(absolute); e == nil {
		absolute = canonical
	} else if !errors.Is(e, os.ErrNotExist) {
		return "", nil, e
	}
	lock, err := os.OpenFile(absolute+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return "", nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return "", nil, fmt.Errorf("local execution ledger is already in use: %w", err)
	}
	return absolute, lock, nil
}

func retireLocalExecution(ctx context.Context, args []string) error {
	f := flag.NewFlagSet("retire-local-execution", flag.ContinueOnError)
	path := f.String("local-db", defaultDB()+".local.db", "existing local execution ledger; main service must be stopped")
	id := f.String("execution", "", "execution ID in reconciliation")
	operator := f.String("operator", "", "operator identity")
	note := f.String("note", "", "Docker inspection and verification that the operation stopped")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || *id == "" || *operator == "" || *note == "" {
		return errors.New("requires -execution, -operator and -note")
	}
	if _, err := os.Stat(*path); err != nil {
		return err
	}
	canonical, lock, err := lockLocalLedger(*path)
	if err != nil {
		return err
	}
	defer lock.Close()
	book, err := ledger.Open(ctx, canonical)
	if err != nil {
		return err
	}
	defer book.Close()
	return book.RetireInterrupted(ctx, *id, *operator, *note)
}
