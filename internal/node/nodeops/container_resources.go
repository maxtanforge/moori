package nodeops

import (
	"context"
	"errors"
	"strings"

	"github.com/docker/docker/api/types/container"

	"github.com/maxtanforge/moori/internal/core/nodeops"
)

func containerLimitValues(resources container.Resources) (cpu, memory, pids int64) {
	cpu, memory = resources.NanoCPUs, resources.Memory
	if cpu == 0 && resources.CPUQuota > 0 {
		period := resources.CPUPeriod
		if period <= 0 {
			period = 100_000
		}
		cpu = int64(float64(resources.CPUQuota) / float64(period) * 1e9)
	}
	if resources.PidsLimit != nil {
		pids = max(0, *resources.PidsLimit)
	}
	return
}

func applyContainerResources(target *container.Resources, edit *nodeops.ContainerResources) {
	if edit == nil {
		return
	}
	if edit.NanoCPUs != nil {
		target.NanoCPUs = *edit.NanoCPUs
		target.CPUQuota, target.CPUPeriod = 0, 0
	}
	if edit.MemoryBytes != nil {
		target.Memory = *edit.MemoryBytes
		if target.Memory == 0 && target.MemorySwap > 0 {
			target.MemorySwap = 0
		}
	}
	if edit.PIDs != nil {
		pids := *edit.PIDs
		if pids == 0 {
			pids = -1
		}
		target.PidsLimit = &pids
	}
}

func (r Runner) updateContainerResources(ctx context.Context, req nodeops.Request) (nodeops.Result, error) {
	current, err := r.exactContainer(ctx, req.Target)
	if err != nil {
		return nodeops.Result{}, err
	}
	if current.Config == nil || current.HostConfig == nil {
		return nodeops.Result{}, errors.New("Docker did not return the container configuration")
	}
	if current.Config.Labels["com.docker.compose.project"] != "" || current.Config.Labels["com.docker.swarm.service.id"] != "" {
		return nodeops.Result{}, errors.New("edit this container through its Compose project or service")
	}
	if req.Resources.NanoCPUs != nil && (current.HostConfig.CPUPeriod != 0 || current.HostConfig.CPUQuota != 0) {
		return nodeops.Result{}, errors.New("existing CPU quota settings require container replacement")
	}
	var update container.UpdateConfig
	applyContainerResources(&update.Resources, req.Resources)
	if req.Resources.MemoryBytes != nil {
		swap := current.HostConfig.MemorySwap
		if swap > 0 && swap < update.Memory {
			return nodeops.Result{}, errors.New("the memory limit exceeds the existing combined memory and swap limit")
		}
		if swap == 0 {
			// Docker uses twice the configured memory for the default combined limit.
			swap = 2 * update.Memory
		}
		update.MemorySwap = swap
	}
	response, err := r.Docker.ContainerUpdate(ctx, req.Target, update)
	if err != nil {
		return nodeops.Result{}, dockerMutationError(err)
	}
	if len(response.Warnings) > 0 {
		return nodeops.Result{}, nodeops.ErrOutcomeUnknown
	}
	result := r.confirmedContainerResult(ctx, nodeops.Container{
		FullID: req.Target, Name: strings.TrimPrefix(current.Name, "/"), Image: current.Config.Image,
	})
	edit := req.Resources
	if result.ResourceObservedAt == "" || edit.NanoCPUs != nil && result.Container.NanoCPUs != *edit.NanoCPUs ||
		edit.MemoryBytes != nil && result.Container.MemoryLimitBytes != *edit.MemoryBytes ||
		edit.PIDs != nil && result.Container.PIDsLimit != *edit.PIDs {
		return nodeops.Result{}, nodeops.ErrOutcomeUnknown
	}
	return result, nil
}
