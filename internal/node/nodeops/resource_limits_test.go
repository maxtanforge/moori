package nodeops

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/errdefs"

	"github.com/maxtanforge/moori/internal/core/nodeops"
)

type resourceLimitsDocker struct {
	Docker
	host    container.HostConfig
	config  container.Config
	applied bool
	opts    volume.CreateOptions
}

func (d *resourceLimitsDocker) ContainerInspect(_ context.Context, id string) (container.InspectResponse, error) {
	return container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{
		ID: id, Name: "/web", HostConfig: &d.host, State: &container.State{Running: true, Status: "running"},
	}, Config: &d.config}, nil
}

func (d *resourceLimitsDocker) ContainerUpdate(_ context.Context, _ string, update container.UpdateConfig) (container.UpdateResponse, error) {
	d.applied = true
	d.host.Resources = update.Resources
	return container.UpdateResponse{}, nil
}

func (d *resourceLimitsDocker) VolumeInspect(context.Context, string) (volume.Volume, error) {
	return volume.Volume{}, errdefs.NotFound(errors.New("missing"))
}

func (d *resourceLimitsDocker) VolumeCreate(_ context.Context, opts volume.CreateOptions) (volume.Volume, error) {
	d.opts = opts
	return volume.Volume{Name: opts.Name, Driver: opts.Driver, Options: opts.DriverOpts}, nil
}

func TestUpdateStandaloneContainerResourcesWithoutReplacement(t *testing.T) {
	d := &resourceLimitsDocker{}
	cpu, memory, pids := int64(500_000_000), int64(128<<20), int64(0)
	id := strings.Repeat("a", 64)
	req := nodeops.Request{Action: nodeops.ContainerResourcesUpdate, Target: id,
		Resources: &nodeops.ContainerResources{NanoCPUs: &cpu, MemoryBytes: &memory, PIDs: &pids}}
	result, err := (Runner{Docker: d}).Run(t.Context(), req)
	if err != nil || !d.applied || result.Container == nil || result.Container.FullID != id ||
		result.Container.PIDsLimit != 0 || result.Container.MemoryLimitBytes != memory || result.Container.NanoCPUs != cpu {
		t.Fatalf("confirmed resource update: %+v, %v", result, err)
	}
	if d.host.MemorySwap != 2*memory || d.host.PidsLimit == nil || *d.host.PidsLimit != -1 {
		t.Fatalf("Docker update sent unexpected resources: %+v", d.host.Resources)
	}
	d.config.Labels = map[string]string{"com.docker.compose.project": "stack"}
	d.applied = false
	if _, err = (Runner{Docker: d}).Run(t.Context(), req); err == nil || d.applied {
		t.Fatal("Compose-owned resources were modified", err)
	}
}

func TestCreateTmpfsVolumeReportsCapacity(t *testing.T) {
	d := &resourceLimitsDocker{}
	result, err := (Runner{Docker: d}).Run(t.Context(), nodeops.Request{
		Action: nodeops.VolumeCreate, Name: "scratch", TmpfsSizeMiB: 128,
	})
	if err != nil || result.Volume == nil || !result.Volume.Tmpfs || result.Volume.CapacityBytes != 128<<20 {
		t.Fatalf("volume: %+v, %v", result, err)
	}
	if d.opts.Driver != "local" || d.opts.DriverOpts["type"] != "tmpfs" || d.opts.DriverOpts["device"] != "tmpfs" || d.opts.DriverOpts["o"] != "size=128m" {
		t.Fatalf("Docker create options: %+v", d.opts)
	}
}
