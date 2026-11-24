package localinv

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
)

type fakeDocker struct {
	containers []container.Summary
	images     []image.Summary
	networks   []network.Summary
	volumes    volume.ListResponse
	usage      types.DiskUsage
	err        error
}

func (f *fakeDocker) ContainerList(ctx context.Context, _ container.ListOptions) ([]container.Summary, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.containers, nil
}

func (f *fakeDocker) ImageList(ctx context.Context, _ image.ListOptions) ([]image.Summary, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.images, nil
}

func (f *fakeDocker) NetworkList(ctx context.Context, _ network.ListOptions) ([]network.Summary, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.networks, nil
}

func (f *fakeDocker) VolumeList(ctx context.Context, _ volume.ListOptions) (volume.ListResponse, error) {
	if f.err != nil {
		return volume.ListResponse{}, f.err
	}
	return f.volumes, nil
}

func (f *fakeDocker) DiskUsage(ctx context.Context, _ types.DiskUsageOptions) (types.DiskUsage, error) {
	if f.err != nil {
		return types.DiskUsage{}, f.err
	}
	return f.usage, nil
}

func fixture() *fakeDocker {
	return &fakeDocker{
		containers: []container.Summary{
			{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Names: []string{"/web"}, Image: "nginx:1.27", State: "running", Status: "Up 2 hours"},
			{ID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Names: []string{"/db"}, Image: "postgres:16", State: "running", Status: "Up 3 days"},
			{ID: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", Names: []string{"/worker"}, Image: "busybox", State: "exited", Status: "Exited (0) 5 minutes ago"},
		},
		images: []image.Summary{
			{ID: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", RepoTags: []string{"nginx:1.27"}, Size: 188 << 20},
			{ID: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", RepoTags: []string{"<none>:<none>"}, Size: 4 << 20},
		},
		networks: []network.Summary{
			{Name: "bridge", Driver: "bridge", Scope: "local"},
			{Name: "moori-net", Driver: "bridge", Scope: "local", Internal: true},
		},
		volumes: volume.ListResponse{Volumes: []*volume.Volume{
			{Name: "moori-data", Driver: "local"},
			{Name: "pg-data", Driver: "local", UsageData: &volume.UsageData{Size: 512 << 20}},
		}},
		usage: types.DiskUsage{
			LayersSize: 192 << 20,
			Images:     []*image.Summary{{Size: 188 << 20}, {Size: 4 << 20}},
			Containers: []*container.Summary{{SizeRw: 3 << 20}, {SizeRw: 1 << 20}},
			Volumes:    []*volume.Volume{{UsageData: &volume.UsageData{Size: 512 << 20}}},
			BuildCache: []*build.CacheRecord{{Size: 64 << 20}},
		},
	}
}

func TestCollectAggregatesCountsAndDiskUsage(t *testing.T) {
	overview, err := collect(context.Background(), fixture())
	if err != nil {
		t.Fatal(err)
	}
	if !overview.Available || overview.ContainerCount != 3 || overview.RunningCount != 2 || overview.StoppedCount != 1 {
		t.Fatal(overview.ContainerCount, overview.RunningCount, overview.StoppedCount)
	}
	if overview.ImageCount != 2 || overview.NetworkCount != 2 || overview.VolumeCount != 2 {
		t.Fatal(overview.ImageCount, overview.NetworkCount, overview.VolumeCount)
	}
	if len(overview.Containers) != 3 || overview.Containers[0].Name != "db" || overview.Containers[0].ID != "bbbbbbbbbbbb" {
		t.Fatal(overview.Containers)
	}
	if overview.Containers[2].Name != "worker" || overview.Containers[2].State != "exited" || overview.Containers[2].Status == "" {
		t.Fatal(overview.Containers[2])
	}
	// Dangling "<none>:<none>" tags are hidden from the console list.
	if len(overview.Images) != 2 || overview.Images[0].Tags[0] != "nginx:1.27" || len(overview.Images[1].Tags) != 0 {
		t.Fatal(overview.Images)
	}
	usage := overview.DiskUsage
	if usage.Layers != 192<<20 || usage.Images != 192<<20 || usage.Containers != 4<<20 || usage.Volumes != 512<<20 || usage.BuildCache != 64<<20 {
		t.Fatal(usage)
	}
	// Usage data stays attached to its own entry: "moori-data" has none, "pg-data" does.
	if overview.Volumes[0].Known || overview.Volumes[1].Size != 512<<20 {
		t.Fatal(overview.Volumes)
	}
}

func TestCollectKeepsDiskSizesIndependentOfDockerReport(t *testing.T) {
	docker := fixture()
	row := &container.Summary{ID: docker.containers[0].ID, SizeRw: 4 << 20}
	docker.usage.Containers = []*container.Summary{row}
	overview, err := collect(context.Background(), docker)
	if err != nil {
		t.Fatal(err)
	}
	row.SizeRw = 8 << 20
	for _, item := range overview.Containers {
		if item.FullID == row.ID && item.WritableBytes != nil && *item.WritableBytes == 4<<20 {
			return
		}
	}
	t.Fatal("cached snapshot did not retain the original writable size")
}

func TestOverviewServesStaleSnapshotAfterFailure(t *testing.T) {
	fake := fixture()
	inv := New(fake)
	inv.ttl = time.Millisecond
	warm := inv.Overview(context.Background())
	if !warm.Available {
		t.Fatal(warm)
	}
	time.Sleep(2 * time.Millisecond)
	fake.err = errors.New("socket closed")
	stale := inv.Overview(context.Background())
	if !stale.Available || stale.Warning == "" {
		t.Fatal("stale snapshot must stay available with a warning", stale)
	}
	if stale.ContainerCount != warm.ContainerCount {
		t.Fatal("stale snapshot lost data")
	}
}

func TestOverviewUnavailableWithoutSnapshot(t *testing.T) {
	inv := New(&fakeDocker{err: errors.New("permission denied")})
	overview := inv.Overview(context.Background())
	if overview.Available || overview.Message == "" || overview.ContainerCount != 0 {
		t.Fatal(overview)
	}
}

func TestPrefetchKeepsSnapshotWithinTTL(t *testing.T) {
	fake := fixture()
	inv := New(fake)
	inv.Prefetch(context.Background())
	fake.err = errors.New("daemon restarted")
	overview := inv.Overview(context.Background())
	if !overview.Available || overview.Warning != "" {
		t.Fatal(overview)
	}
}
