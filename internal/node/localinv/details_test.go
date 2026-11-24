package localinv

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"strings"
	"testing"
	"time"
)

type imageFailure struct {
	*fakeDocker
	fail bool
}

func (d *imageFailure) ImageList(ctx context.Context, o image.ListOptions) ([]image.Summary, error) {
	if d.fail {
		return nil, errors.New("unavailable")
	}
	return d.fakeDocker.ImageList(ctx, o)
}
func TestIndependentSections(t *testing.T) {
	d := &imageFailure{fakeDocker: fixture()}
	i := New(d)
	i.ttl = 0
	first := i.Overview(context.Background())
	d.fail = true
	d.containers = nil
	got := i.Overview(context.Background())
	if got.ContainerCount != 0 || got.ImageCount != 2 || !got.SectionTimes["images"].Equal(first.SectionTimes["images"]) || got.Host == nil || got.Warning == "" {
		t.Fatal(got)
	}
}
func TestJSONBudgetIncludesNestedFields(t *testing.T) {
	d := fixture()
	d.images[0].RepoTags = []string{strings.Repeat("<", MaxSnapshotBytes)}
	o := New(d).Overview(context.Background())
	b, e := json.Marshal(o)
	if e != nil || len(b) > MaxSnapshotBytes || !o.Truncated || o.ImageCount != 2 || len(o.Images) != 1 || len(o.Containers) != 3 {
		t.Fatal(len(b), e, o.Truncated)
	}
}
func TestStatsWorkingSetAndCPU(t *testing.T) {
	var raw container.StatsResponse
	raw.Read = time.Now()
	raw.PreRead = raw.Read.Add(-time.Second)
	raw.PreCPUStats.SystemUsage = 100
	raw.PreCPUStats.CPUUsage.TotalUsage = 20
	raw.CPUStats.SystemUsage = 200
	raw.CPUStats.CPUUsage.TotalUsage = 45
	raw.CPUStats.OnlineCPUs = 4
	raw.MemoryStats.Usage = 200
	raw.MemoryStats.Stats = map[string]uint64{"inactive_file": 30}
	raw.Networks = map[string]container.NetworkStats{"eth0": {RxBytes: 4}, "eth1": {RxBytes: 5}}
	s := statsFrom(raw)
	if s.CPUPercent == nil || *s.CPUPercent != 100 || s.MemoryBytes != 170 || s.RxBytes != 9 {
		t.Fatal(s)
	}
	raw.CPUStats.SystemUsage = 80
	if statsFrom(raw).CPUPercent != nil {
		t.Fatal("counter reset should omit CPU")
	}
}
