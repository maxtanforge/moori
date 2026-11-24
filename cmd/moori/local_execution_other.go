//go:build !linux

package main

import (
	"context"
	"errors"
	"github.com/maxtanforge/moori/internal/control/localexec"
)

func startLocalExecution(_ context.Context, mode, _, _, _ string, _ localexec.Options) (localRuntime, error) {
	r := localRuntime{message: "本机 Docker 部署需要 Linux 主机。", close: func() {}}
	if mode == "on" {
		return r, errors.New("local Docker execution requires Linux")
	}
	if mode != "off" && mode != "auto" {
		return r, errors.New("local-execution must be auto, on or off")
	}
	return r, nil
}

func retireLocalExecution(context.Context, []string) error {
	return errors.New("local Docker execution requires Linux")
}
