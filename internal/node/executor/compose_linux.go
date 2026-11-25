package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// LocalCompose uses the local Docker socket and a trusted Docker executable.
// Inherited Docker and Compose environment overrides are not forwarded.
func LocalCompose(dockerPath string) Apply {
	return func(ctx context.Context, plan Plan) error {
		const processGrace = 5 * time.Second
		ctx, cancel := context.WithTimeout(ctx, plan.Timeout+processGrace)
		defer cancel()
		inspectCtx, done := context.WithTimeout(ctx, 10*time.Second)
		err := checkExternalResources(inspectCtx, plan.Compose, dockerInspectResources(dockerPath))
		done()
		if err != nil {
			if ctx.Err() != nil {
				return errors.Join(ErrStoppedBeforeLaunch, ctx.Err())
			}
			return errors.Join(ErrLaunchFailed, err)
		}
		cmd := exec.CommandContext(ctx, dockerPath, composeArgs(plan)...)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
		cmd.Stdin = strings.NewReader(plan.Compose)
		cmd.Stdout, cmd.Stderr = plan.Output, plan.Output
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			} else {
				return err
			}
		}
		cmd.WaitDelay = processGrace
		if err := cmd.Start(); err != nil {
			if ctx.Err() != nil {
				return errors.Join(ErrStoppedBeforeLaunch, err)
			}
			return errors.Join(ErrLaunchFailed, fmt.Errorf("start Docker Compose: %w", err))
		}
		if err := cmd.Wait(); err != nil {
			// The CLI can exit after sending requests that the daemon still has in
			// flight. Keep the target locked until its actual state is inspected.
			return errors.Join(ErrOutcomeUnknown, fmt.Errorf("Docker Compose: %w", err))
		}
		return nil
	}
}

func dockerInspectResources(dockerPath string) inspectResources {
	return func(ctx context.Context, kind string, names []string) ([]byte, error) {
		format := `{"name":{{json .Name}},"driver":{{json .Driver}},"scope":{{json .Scope}}`
		if kind == "volume" {
			format += `,"options":{{len .Options}}`
		}
		args := []string{"--host", "unix:///var/run/docker.sock", kind, "inspect", "--format", format + "}", "--"}
		cmd := exec.CommandContext(ctx, dockerPath, append(args, names...)...)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
		return cmd.Output()
	}
}
