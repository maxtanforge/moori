package execstream

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"

	wire "github.com/maxtanforge/moori/internal/transport/execstream"
)

// DockerExec is the subset of the Docker client that exec sessions need.
// *client.Client satisfies it directly; tests stand in with a fake.
type DockerExec interface {
	ContainerInspect(ctx context.Context, container string) (container.InspectResponse, error)
	ContainerExecCreate(ctx context.Context, container string, config container.ExecOptions) (container.ExecCreateResponse, error)
	ContainerExecAttach(ctx context.Context, execID string, config container.ExecAttachOptions) (types.HijackedResponse, error)
	ContainerExecResize(ctx context.Context, execID string, options container.ResizeOptions) error
	ContainerExecInspect(ctx context.Context, execID string) (container.ExecInspect, error)
}

// DockerStarter returns a Starter bound to the local Docker daemon.
func DockerStarter(cli DockerExec) Starter {
	return func(ctx context.Context, containerID string, cols, rows int) (Session, error) {
		return StartDockerSession(ctx, cli, containerID, cols, rows)
	}
}

// dockerSession implements Session over a hijacked exec connection.
type dockerSession struct {
	conn   *execConn
	execID string
	cli    DockerExec
}

func (s *dockerSession) Read(p []byte) (int, error) { return s.conn.Read(p) }

// Write bounds each keystroke write so a stalled daemon cannot wedge the
// relay pump.
func (s *dockerSession) Write(p []byte) (int, error) {
	if err := s.conn.conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return 0, err
	}
	return s.conn.Write(p)
}

func (s *dockerSession) Close() error { return s.conn.Close() }

func (s *dockerSession) Resize(ctx context.Context, cols, rows int) error {
	return s.cli.ContainerExecResize(ctx, s.execID, container.ResizeOptions{Height: uint(rows), Width: uint(cols)})
}

// ExitCode returns the exec result; the daemon clears the metadata shortly
// after termination, and unknown states answer -1.
func (s *dockerSession) ExitCode(ctx context.Context) int {
	ins, err := s.cli.ContainerExecInspect(ctx, s.execID)
	if err != nil || ins.Running {
		return -1
	}
	return ins.ExitCode
}

// StartDockerSession creates an interactive TTY exec inside the container.
// The shell follows the container's common availability: bash first, sh as
// the fallback.
func StartDockerSession(ctx context.Context, cli DockerExec, containerID string, cols, rows int) (Session, error) {
	if cli == nil {
		return nil, errors.New("docker daemon is not reachable")
	}
	if !wire.ValidContainer(containerID) {
		return nil, wire.ErrInvalid
	}
	if cols <= 0 {
		cols = wire.ColDefault
	}
	if rows <= 0 {
		rows = wire.RowDefault
	}
	inspect, err := cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return nil, err
	}
	if inspect.State == nil || !inspect.State.Running || inspect.State.Paused || inspect.State.Restarting {
		return nil, errors.New("container must be running to open a terminal")
	}
	// Resolve the preferred shell inside the container before starting it.
	// Docker can acknowledge exec creation before detecting a missing binary.
	shells := [][]string{
		{"/bin/sh", "-c", "if [ -x /bin/bash ]; then exec /bin/bash; fi; exec /bin/sh"},
		{"/bin/bash"},
	}
	for _, command := range shells {
		created, err := cli.ContainerExecCreate(ctx, inspect.ID, container.ExecOptions{
			Cmd:          command,
			Tty:          true,
			AttachStdin:  true,
			AttachStdout: true,
			AttachStderr: true,
			Env:          []string{"TERM=xterm"},
		})
		if err != nil {
			continue
		}
		resp, err := cli.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{Tty: true})
		if err != nil {
			continue
		}
		return &dockerSession{
			conn:   &execConn{conn: resp.Conn, reader: bufio.NewReader(resp.Reader)},
			execID: created.ID,
			cli:    cli,
		}, nil
	}
	return nil, fmt.Errorf("no usable shell in container %s", containerID[:12])
}

var _ io.ReadWriteCloser = (*execConn)(nil)
