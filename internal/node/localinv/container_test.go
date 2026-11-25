package localinv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/system"
)

type inspectFixture struct {
	*fakeDocker
	failed bool
	raw    container.StatsResponse
}

func (d *inspectFixture) ContainerInspect(context.Context, string) (container.InspectResponse, error) {
	if d.failed {
		return container.InspectResponse{}, errors.New("inspect")
	}
	cfg := &container.HostConfig{RestartPolicy: container.RestartPolicy{Name: "always"}}
	cfg.Memory = 512
	cfg.NanoCPUs = 1000000000
	return container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{HostConfig: cfg, State: &container.State{Status: "running", Health: &container.Health{Status: "healthy"}}}, Config: &container.Config{Env: []string{"SECRET=private-inspect-value"}}}, nil
}
func (d *inspectFixture) ContainerStatsOneShot(context.Context, string) (container.StatsResponseReader, error) {
	if d.failed {
		return container.StatsResponseReader{}, errors.New("stats")
	}
	b, err := json.Marshal(d.raw)
	return container.StatsResponseReader{Body: io.NopCloser(bytes.NewReader(b))}, err
}
func (d *inspectFixture) Info(context.Context) (system.Info, error) {
	return system.Info{Name: "test-engine", Driver: "overlay2", Labels: []string{"secret=private-inspect-value"}}, nil
}
func (d *inspectFixture) ServerVersion(context.Context) (types.Version, error) {
	return types.Version{Version: "test", APIVersion: "1.48"}, nil
}

func TestRepeatedContainerSamplesAndInspectFailure(t *testing.T) {
	d := &inspectFixture{fakeDocker: fixture()}
	d.containers = d.containers[:1]
	d.usage.Containers[0].ID = d.containers[0].ID
	d.raw.Read = time.Now().UTC()
	d.raw.CPUStats.SystemUsage = 200
	d.raw.CPUStats.CPUUsage.TotalUsage = 40
	d.raw.CPUStats.OnlineCPUs = 4
	inv := New(d)
	inv.ttl = 0
	ctx := context.Background()
	first := inv.Overview(ctx)
	c := first.Containers[0]
	if c.WritableBytes == nil || *c.WritableBytes != 3<<20 || first.Engine.APIVersion != "1.48" || c.Stats == nil || c.Stats.CPUPercent != nil || c.Health != "healthy" || c.RestartPolicy != "always" || c.MemoryLimitBytes != 512 || c.NanoCPUs != 1000000000 {
		t.Fatal(first)
	}
	data, _ := json.Marshal(first)
	if bytes.Contains(data, []byte("private-inspect-value")) {
		t.Fatal("inspect configuration leaked")
	}
	d.raw.Read = d.raw.Read.Add(time.Second)
	d.raw.CPUStats.SystemUsage += 100
	d.raw.CPUStats.CPUUsage.TotalUsage += 10
	second := inv.Overview(ctx).Containers[0]
	if second.Stats.CPUPercent == nil || *second.Stats.CPUPercent != 40 {
		t.Fatal(second)
	}
	d.failed = true
	failed := inv.Overview(ctx)
	last := failed.Containers[0]
	if failed.Warning == "" || !last.ConfigSampledAt.Equal(second.ConfigSampledAt) || last.Health != second.Health || !last.Stats.SampledAt.Equal(second.Stats.SampledAt) {
		t.Fatal(failed)
	}
	d.containers[0].State = "exited"
	stopped := inv.Overview(ctx).Containers[0]
	if stopped.State != "exited" || stopped.Health != "" {
		t.Fatalf("stopped container inherited old health: %+v", stopped)
	}
}

func TestListRefreshSkipsHeavySamplesAndPreservesTheirValues(t *testing.T) {
	d := &inspectFixture{fakeDocker: fixture()}
	d.containers = d.containers[:1]
	d.usage.Containers[0].ID = d.containers[0].ID
	d.raw.Read = time.Now().UTC()
	inv := New(d)
	before := inv.Refresh(t.Context())
	d.containers[0].Names = []string{"/renamed"}
	d.containers[0].Status = "Up 1 minute (unhealthy)"
	d.failed = true
	inv.disk.at = inv.disk.at.Add(-2 * diskReuseWindow)
	d.usage.LayersSize++
	after := inv.RefreshLists(t.Context())
	c := after.Containers[0]
	if after.Warning != "" || c.Name != "renamed" || c.Health != "unhealthy" || c.Stats != before.Containers[0].Stats || c.ConfigSampledAt != before.Containers[0].ConfigSampledAt || c.WritableBytes == nil {
		t.Fatal(after)
	}
	for _, key := range []string{"host", "filesystems", "engine", "disk_usage"} {
		if before.SectionTimes[key] != after.SectionTimes[key] {
			t.Fatalf("list refresh resampled %s", key)
		}
	}
	if before.DiskUsage.Layers != after.DiskUsage.Layers || !after.DockerSampledAt().After(before.DockerSampledAt()) {
		t.Fatal(after)
	}
}
