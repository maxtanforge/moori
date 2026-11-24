package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/maxtanforge/moori/internal/node/ledger"
	"github.com/maxtanforge/moori/internal/node/secrets"
	"github.com/maxtanforge/moori/internal/platform/sqlite"
)

func gcSecrets(ctx context.Context, args []string) error {
	f := flag.NewFlagSet("gc-secrets", flag.ContinueOnError)
	db := f.String("db", sqlite.DefaultPath("agent.db"), "existing Agent ledger; Agent must be stopped")
	root := f.String("secret-dir", "", "secret root (default <db>.secrets)")
	docker := f.String("docker", "docker", "trusted local Docker executable")
	age := f.Duration("min-age", 24*time.Hour, "minimum bundle age")
	apply := f.Bool("apply", false, "remove unused bundles; otherwise list only")
	quiet := f.Bool("docker-quiescent", false, "confirm external container and mount changes are paused")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || *age < 0 || !*quiet {
		return errors.New("requires -docker-quiescent and nonnegative -min-age")
	}
	if _, err := os.Stat(*db); err != nil {
		return err
	}
	lock, err := lockLedger(*db)
	if err != nil {
		return err
	}
	defer lock.Close()
	book, err := ledger.Open(ctx, *db)
	if err != nil {
		return err
	}
	defer book.Close()
	active, err := book.Active(ctx, "", 1)
	if err != nil {
		return err
	}
	if len(active) > 0 {
		return errors.New("resolve active ledger executions before collecting secrets")
	}
	path, err := exec.LookPath(*docker)
	if err != nil {
		return err
	}
	inventoryCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	ids, err := dockerOutput(inventoryCtx, path, "container", "ls", "--all", "--quiet", "--no-trunc")
	if err != nil {
		return err
	}
	var mounts []string
	for _, id := range strings.Fields(string(ids)) {
		if len(id) != 64 || strings.Trim(id, "0123456789abcdef") != "" {
			return errors.New("invalid Docker container ID")
		}
		raw, err := dockerOutput(inventoryCtx, path, "container", "inspect", "--format", "{{json .Mounts}}", id)
		if err != nil {
			return err
		}
		var rows []struct{ Source string }
		if err := json.Unmarshal(raw, &rows); err != nil {
			return err
		}
		for _, row := range rows {
			if row.Source != "" {
				mounts = append(mounts, row.Source)
			}
		}
	}
	after, err := dockerOutput(inventoryCtx, path, "container", "ls", "--all", "--quiet", "--no-trunc")
	if err != nil {
		return err
	}
	if !bytes.Equal(ids, after) {
		return errors.New("Docker inventory changed during inspection; retry with mutations paused")
	}
	if *root == "" {
		*root = *db + ".secrets"
	}
	removed, err := secrets.CollectUnused(*root, mounts, time.Now().Add(-*age), *apply)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		Applied bool     `json:"applied"`
		Bundles []string `json:"bundles"`
	}{*apply, removed})
}

func dockerOutput(ctx context.Context, path string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, path, append([]string{"--host", "unix:///var/run/docker.sock"}, args...)...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	var out boundedOutput
	cmd.Stdout = &out
	cmd.Stderr = &boundedOutput{}
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
