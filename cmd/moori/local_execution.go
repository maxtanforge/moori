package main

import (
	"github.com/maxtanforge/moori/internal/control/human"
	"github.com/maxtanforge/moori/internal/transport/execstream"
)

type localRuntime struct {
	enabled       bool
	message       string
	done          <-chan error
	followLogs    human.FollowContainerLogs
	startTerminal execstream.StartTerminal
	runOperation  human.RunLocalNodeOperation
	close         func()
}
