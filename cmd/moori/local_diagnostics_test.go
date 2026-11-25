package main

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
)

func TestLocalDockerDiagnosticsSeparateSocketAndPluginFailures(t *testing.T) {
	permission := &net.OpError{Op: "dial", Net: "unix", Err: &os.SyscallError{Syscall: "connect", Err: os.ErrPermission}}
	cases := []struct {
		stage        string
		cause        error
		output, want string
	}{
		{"docker socket", permission, "", "UID 10001 无权访问"},
		{"docker socket", &os.PathError{Op: "dial", Path: "/var/run/docker.sock", Err: os.ErrNotExist}, "", "未找到 /var/run/docker.sock"},
		{"docker executable", os.ErrNotExist, "", "Docker CLI"},
		{"docker compose", errors.New("exit status 1"), "compose: plugin failed to load", "compose: plugin failed to load"},
		{"docker compose", context.DeadlineExceeded, "", "检测超时"},
	}
	for _, tc := range cases {
		err := &localDockerError{stage: tc.stage, cause: tc.cause, output: tc.output}
		if text := localDockerMessage(err, 10001); !strings.Contains(text, tc.want) {
			t.Errorf("%s: %q lacks %q", tc.stage, text, tc.want)
		}
		if !errors.Is(err, tc.cause) {
			t.Fatal("lost original diagnostic cause")
		}
	}
}

func TestComposeDiagnosticOutputBoundedAndDrained(t *testing.T) {
	var output probeOutput
	for range 3 {
		p := []byte(strings.Repeat("x", 3000))
		if n, err := output.Write(p); err != nil || n != len(p) {
			t.Fatal(n, err)
		}
	}
	if len(output.data) != 4096 {
		t.Fatal(len(output.data))
	}
}
