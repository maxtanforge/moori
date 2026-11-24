package nodeops

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"

	operation "github.com/maxtanforge/moori/internal/core/nodeops"
)

type followDocker struct {
	Docker
	actual  string
	tty     bool
	logs    io.ReadCloser
	calls   int
	options container.LogsOptions
}

func (d *followDocker) ContainerInspect(_ context.Context, _ string) (container.InspectResponse, error) {
	return container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{ID: d.actual, State: &container.State{StartedAt: "2026-11-23T10:11:12.123456789Z"}},
		Config:            &container.Config{Tty: d.tty},
	}, nil
}

func (d *followDocker) ContainerLogs(_ context.Context, _ string, opts container.LogsOptions) (io.ReadCloser, error) {
	d.calls++
	d.options = opts
	return d.logs, nil
}

func TestLogSnapshotUsesLatestStartUnlessAllRunsRequested(t *testing.T) {
	id := strings.Repeat("b", 64)
	docker := &followDocker{actual: id, tty: true}
	runner := Runner{Docker: docker}
	for _, allRuns := range []bool{false, true} {
		docker.logs = io.NopCloser(strings.NewReader("output\n"))
		result, err := runner.container(t.Context(), operation.Request{Action: operation.ContainerLogs, Target: id, AllLogRuns: allRuns})
		if err != nil || result.Log != "output\n" {
			t.Fatalf("snapshot: %+v, %v", result, err)
		}
		want := "2026-11-23T10:11:12.123456789Z"
		if allRuns {
			want = ""
		}
		if docker.options.Since != want || docker.options.Follow {
			t.Fatalf("all runs %v: %+v", allRuns, docker.options)
		}
	}
}

func TestFollowLogsRequiresAnExactContainerAndUsesRawTTY(t *testing.T) {
	id := strings.Repeat("b", 64)
	docker := &followDocker{actual: strings.Repeat("a", 64), tty: true, logs: io.NopCloser(strings.NewReader("terminal\n"))}
	runner := Runner{Docker: docker}
	if _, err := runner.FollowLogs(t.Context(), id[:12], 100, false, false); !errors.Is(err, operation.ErrInvalid) {
		t.Fatalf("short ID = %v", err)
	}
	if _, err := runner.FollowLogs(t.Context(), id, 1, false, false); !errors.Is(err, operation.ErrInvalid) {
		t.Fatalf("short tail = %v", err)
	}
	if _, err := runner.FollowLogs(t.Context(), id, 100, false, false); err == nil || docker.calls != 0 {
		t.Fatalf("mismatched identity reached ContainerLogs: %v, calls=%d", err, docker.calls)
	}
	docker.actual = id
	reader, err := runner.FollowLogs(t.Context(), id, 0, true, false)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	got, err := io.ReadAll(reader)
	if err != nil || string(got) != "terminal\n" {
		t.Fatalf("raw TTY = %q, %v", got, err)
	}
	if !docker.options.Follow || !docker.options.ShowStdout || !docker.options.ShowStderr || docker.options.Tail != strconv.Itoa(operation.DefaultLogLines) {
		t.Fatalf("bad Docker options: %+v", docker.options)
	}
	if !docker.options.Timestamps || docker.options.Since != "2026-11-23T10:11:12.123456789Z" {
		t.Fatalf("timestamps and current run boundary did not reach Docker: %+v", docker.options)
	}
}

func TestFollowLogsEmitsPartialFrameAndClosesOnCancellation(t *testing.T) {
	id := strings.Repeat("a", 64)
	source, writer := io.Pipe()
	defer writer.Close()
	runner := Runner{Docker: &followDocker{actual: id, logs: source}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reader, err := runner.FollowLogs(ctx, id, 200, false, true)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	partial := make(chan error, 1)
	go func() {
		header := logFrame(2, 1<<20)
		if _, err := writer.Write(append(header, []byte("abc")...)); err != nil {
			partial <- err
			return
		}
		_, err := writer.Write([]byte("next"))
		partial <- err
	}()
	result := make(chan string, 1)
	go func() {
		var data [10]byte
		n, _ := reader.Read(data[:])
		result <- string(data[:n])
	}()
	select {
	case data := <-result:
		if data != "abc" {
			t.Fatalf("partial frame = %q", data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("read buffered the advertised frame before producing bytes")
	}
	cancel()
	select {
	case err := <-partial:
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("source remained open: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not close Docker's stream")
	}
}

func TestFollowLogsDemultiplexesStdoutAndStderr(t *testing.T) {
	id := strings.Repeat("a", 64)
	var raw bytes.Buffer
	for _, frame := range []struct {
		stream byte
		text   string
	}{{1, "out\n"}, {2, "err\n"}} {
		raw.Write(logFrame(frame.stream, len(frame.text)))
		raw.WriteString(frame.text)
	}
	runner := Runner{Docker: &followDocker{actual: id, logs: io.NopCloser(&raw)}}
	reader, err := runner.FollowLogs(t.Context(), id, 200, false, false)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	output, err := io.ReadAll(reader)
	if err != nil || string(output) != "out\nerr\n" {
		t.Fatalf("demultiplexed logs = %q, %v", output, err)
	}
}

func logFrame(stream byte, length int) []byte {
	header := make([]byte, 8)
	header[0] = stream
	binary.BigEndian.PutUint32(header[4:], uint32(length))
	return header
}
