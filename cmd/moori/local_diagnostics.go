package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

type localDockerError struct {
	stage  string
	cause  error
	output string
}

func (e *localDockerError) Error() string {
	message := fmt.Sprintf("%s: %v", e.stage, e.cause)
	if e.output != "" {
		message += ": " + e.output
	}
	return message
}
func (e *localDockerError) Unwrap() error { return e.cause }

func localDockerMessage(err error, uid int) string {
	var probe *localDockerError
	if !errors.As(err, &probe) {
		return "本机 Docker 检测失败：" + err.Error()
	}
	switch probe.stage {
	case "docker executable":
		return "主服务容器内找不到或无法执行 Docker CLI，请更新为包含 Docker CLI 的主服务镜像。原因：" + probe.cause.Error()
	case "docker socket":
		if errors.Is(probe.cause, os.ErrPermission) {
			return fmt.Sprintf("当前容器用户 UID %d 无权访问 /var/run/docker.sock。请让容器进程加入 socket 所属的宿主机组（使用数字 GID），再重新创建主服务容器。若有显式用户覆盖，请核对它是否仍在生效。", uid)
		}
		if errors.Is(probe.cause, os.ErrNotExist) {
			return "主服务容器内未找到 /var/run/docker.sock，请挂载宿主 Docker socket 后重新创建容器。"
		}
		return "无法连接本机 Docker socket，请检查宿主 Docker 是否运行。原因：" + probe.cause.Error()
	case "docker compose":
		if errors.Is(probe.cause, context.DeadlineExceeded) {
			return "主服务容器内 Docker Compose 检测超时，请检查 CLI 和 Compose 插件后重启主服务。"
		}
		return "主服务容器内 Docker Compose 插件检测失败：" + probe.Error()
	default:
		return "本机 Docker 检测失败：" + probe.Error()
	}
}

// Keep plugin diagnostics bounded while still draining command stderr.
type probeOutput struct{ data []byte }

func (b *probeOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := 4096 - len(b.data)
	if remaining > 0 {
		b.data = append(b.data, p[:min(len(p), remaining)]...)
	}
	return n, nil
}

func probeLocalCompose(ctx context.Context, dockerPath string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, dockerPath, "--host", "unix:///var/run/docker.sock", "compose", "version")
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	command.WaitDelay = time.Second
	var output probeOutput
	command.Stderr = &output
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			err = errors.Join(ctx.Err(), err)
		}
		return &localDockerError{stage: "docker compose", cause: err, output: strings.TrimSpace(strings.ToValidUTF8(string(output.data), "�"))}
	}
	return nil
}
