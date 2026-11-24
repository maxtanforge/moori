package composefiles

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/maxtanforge/moori/internal/node/executor"
)

var serviceName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// Services validates an ordered group with the trusted local Compose CLI.
func (s *Sources) Services(ctx context.Context, dockerPath string, snapshot Snapshot) ([]string, error) {
	return s.services(ctx, dockerPath, snapshot, (*exec.Cmd).Run)
}

func (s *Sources) services(ctx context.Context, dockerPath string, snapshot Snapshot, run func(*exec.Cmd) error) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd, cleanup, err := s.command(ctx, dockerPath, snapshot, "config", "--services")
	if err != nil {
		return nil, err
	}
	defer cleanup()
	var output serviceOutput
	cmd.Stdout = &output
	if err := run(cmd); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("Docker Compose 未通过整组源文件校验，请核对配置和变量")
	}
	names := strings.Fields(output.String())
	if len(names) > 512 {
		return nil, errors.New("Docker Compose 返回的服务过多")
	}
	for _, name := range names {
		if !serviceName.MatchString(name) {
			return nil, errors.New("Docker Compose 返回了无效的服务名称")
		}
	}
	return names, nil
}

// Apply fixes the selected source revision and starts its existing Compose
// project. Only Compose itself determines which services need replacement.
func (s *Sources) Apply(ctx context.Context, dockerPath, revision string, forceRecreate bool) error {
	snapshot, err := s.Read()
	if err != nil {
		return err
	}
	if snapshot.Revision != revision {
		return ErrConflict
	}
	return s.apply(ctx, dockerPath, snapshot, forceRecreate, func(cmd *exec.Cmd) (bool, error) {
		if err := cmd.Start(); err != nil {
			return false, err
		}
		return true, cmd.Wait()
	})
}

func (s *Sources) apply(ctx context.Context, dockerPath string, snapshot Snapshot, forceRecreate bool, run func(*exec.Cmd) (bool, error)) error {
	ctx, cancel := context.WithTimeout(ctx, 125*time.Second)
	defer cancel()
	args := []string{"up", "--detach", "--no-build", "--pull", "missing", "--wait", "--wait-timeout", "120"}
	if forceRecreate {
		args = append(args, "--force-recreate")
	}
	cmd, cleanup, err := s.command(ctx, dockerPath, snapshot, args...)
	if err != nil {
		return err
	}
	defer cleanup()
	started, err := run(cmd)
	if err == nil {
		return nil
	}
	if started {
		return executor.ErrOutcomeUnknown
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.New("无法启动 Docker Compose 应用")
}

// Stage the exact validated bytes so Compose cannot reopen mutable source paths
// while it parses them. Bind paths still resolve from the first registered file.
func (s *Sources) command(ctx context.Context, dockerPath string, snapshot Snapshot, action ...string) (*exec.Cmd, func(), error) {
	if dockerPath == "" {
		return nil, nil, errors.New("Docker Compose 未配置")
	}
	if len(snapshot.Files) != len(s.paths) {
		return nil, nil, ErrInvalidSource
	}
	for i, file := range snapshot.Files {
		if file.Path != s.paths[i] || len(file.Content) > maxFileBytes {
			return nil, nil, ErrInvalidSource
		}
	}
	if err := validateDocuments(snapshot); err != nil {
		return nil, nil, err
	}
	localPath, err := filepath.Localize(s.paths[0])
	if err != nil {
		return nil, nil, ErrInvalidSource
	}
	projectDir := filepath.Join(s.root, filepath.Dir(localPath))
	dir, err := os.MkdirTemp("", "moori-compose-")
	if err != nil {
		return nil, nil, errors.New("无法准备 Docker Compose 校验文件")
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	args := []string{"--host", "unix:///var/run/docker.sock", "compose", "--env-file", os.DevNull,
		"--project-directory", projectDir, "-p", s.project}
	for i, file := range snapshot.Files {
		temp := filepath.Join(dir, fmt.Sprintf("compose-%d.yaml", i))
		if err := os.WriteFile(temp, []byte(file.Content), 0600); err != nil {
			cleanup()
			return nil, nil, errors.New("无法准备 Docker Compose 校验文件")
		}
		args = append(args, "-f", temp)
	}
	cmd := exec.CommandContext(ctx, dockerPath, append(args, action...)...)
	cmd.Dir = projectDir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	cmd.WaitDelay = 5 * time.Second
	configureProcess(cmd)
	return cmd, cleanup, nil
}

type serviceOutput struct{ bytes.Buffer }

func (b *serviceOutput) Write(data []byte) (int, error) {
	if len(data) > 64<<10-b.Len() {
		return 0, errors.New("Docker Compose 服务列表过大")
	}
	return b.Buffer.Write(data)
}
