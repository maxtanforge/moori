package nodeops

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"strconv"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"

	"github.com/maxtanforge/moori/internal/core/nodeops"
)

// FollowLogs opens an incremental log stream for the exact Docker container.
// The caller owns it until Close or context cancellation.
func (r Runner) FollowLogs(ctx context.Context, containerID string, tail int, timestamps, allRuns bool) (io.ReadCloser, error) {
	req := nodeops.Request{Action: nodeops.ContainerLogs, Target: containerID, Tail: tail, Timestamps: timestamps, AllLogRuns: allRuns}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if r.Docker == nil {
		return nil, errors.New("Docker management is unavailable on this node")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	existing, err := r.exactContainer(ctx, containerID)
	if err != nil {
		return nil, err
	}
	since, err := logRunStart(existing, allRuns)
	if err != nil {
		return nil, err
	}
	if tail == 0 {
		tail = nodeops.DefaultLogLines
	}
	source, err := r.Docker.ContainerLogs(ctx, containerID, container.LogsOptions{
		Follow: true, ShowStdout: true, ShowStderr: true, Tail: strconv.Itoa(tail), Timestamps: timestamps, Since: since,
	})
	if err != nil {
		return nil, dockerError(err)
	}
	logs := &followLogStream{Reader: source, source: source}
	if existing.Config == nil || !existing.Config.Tty {
		logs.Reader = &dockerLogReader{source: source}
	}
	logs.stop = context.AfterFunc(ctx, func() { _ = logs.closeSource() })
	return logs, nil
}

func logRunStart(existing container.InspectResponse, allRuns bool) (string, error) {
	if allRuns {
		return "", nil
	}
	if existing.State == nil {
		return "", errors.New("Docker did not report the container start time")
	}
	start, err := time.Parse(time.RFC3339Nano, existing.State.StartedAt)
	if err != nil {
		return "", errors.New("Docker did not report the container start time")
	}
	// Docker uses the zero time for a container that has never started.
	// Such a container has no previous run to exclude.
	if start.IsZero() {
		return "", nil
	}
	return start.Format(time.RFC3339Nano), nil
}

type followLogStream struct {
	io.Reader
	source io.ReadCloser
	stop   func() bool
	once   sync.Once
	err    error
}

func (s *followLogStream) Close() error {
	s.stop()
	return s.closeSource()
}

func (s *followLogStream) closeSource() error {
	s.once.Do(func() { s.err = s.source.Close() })
	return s.err
}

type dockerLogReader struct {
	source    io.Reader
	remaining uint32
	header    [8]byte
}

func (r *dockerLogReader) Read(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	for r.remaining == 0 {
		if _, err := io.ReadFull(r.source, r.header[:]); err != nil {
			return 0, err
		}
		if r.header[0] > 2 || r.header[1] != 0 || r.header[2] != 0 || r.header[3] != 0 {
			return 0, errors.New("invalid Docker log stream")
		}
		r.remaining = binary.BigEndian.Uint32(r.header[4:])
	}
	// The Docker header controls the number of bytes to read, never the
	// allocation size. Expose partial payloads as soon as Docker writes them.
	n, err := r.source.Read(data[:min(len(data), int(min(r.remaining, 32<<10)))])
	r.remaining -= uint32(n)
	if r.remaining != 0 && errors.Is(err, io.EOF) {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}
