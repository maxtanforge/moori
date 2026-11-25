package nodeops

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"strings"

	"github.com/maxtanforge/moori/internal/core/nodeops"
)

func (r Runner) containerEditConfig(ctx context.Context, id string) (nodeops.Result, error) {
	c, err := r.exactContainer(ctx, id)
	if err != nil {
		return nodeops.Result{}, err
	}
	if c.Config == nil {
		return nodeops.Result{}, errors.New("Docker did not return the container configuration")
	}
	if len(c.Config.Env)+len(c.Config.Labels) > 512 {
		return nodeops.Result{}, errors.New("Docker's editable configuration exceeds the operation output limit")
	}
	view := &nodeops.ContainerEditConfigResult{
		Environment: make(map[string]*string, len(c.Config.Env)),
		Labels:      make(map[string]string, len(c.Config.Labels)),
	}
	if c.HostConfig != nil {
		host := c.HostConfig
		cpu, memory, pids := containerLimitValues(host.Resources)
		view.Resources = &nodeops.ContainerResources{NanoCPUs: &cpu, MemoryBytes: &memory, PIDs: &pids}
		view.LegacyCPU = host.CPUPeriod != 0 || host.CPUQuota != 0
	}
	for _, line := range c.Config.Env {
		key, value, set := strings.Cut(line, "=")
		if _, duplicate := view.Environment[key]; duplicate {
			return nodeops.Result{}, errors.New("Docker reported duplicate environment variable names")
		}
		view.Environment[key] = nil
		if set {
			view.Environment[key] = &value
		}
	}
	maps.Copy(view.Labels, c.Config.Labels)
	encoded, err := json.Marshal(view)
	if err != nil || len(encoded) > nodeops.MaxContainerEditConfigBytes {
		return nodeops.Result{}, errors.New("Docker's editable configuration exceeds the operation output limit")
	}
	return nodeops.Result{EditConfig: view}, nil
}
