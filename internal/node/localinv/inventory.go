// Package localinv collects host and Docker inventory for local and remote nodes.
package localinv

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
)

// Docker is the read-only subset of the Docker client the inventory needs.
// *client.Client satisfies it directly, so tests can stand in with a fake.
type Docker interface {
	ContainerList(context.Context, container.ListOptions) ([]container.Summary, error)
	ImageList(context.Context, image.ListOptions) ([]image.Summary, error)
	NetworkList(context.Context, network.ListOptions) ([]network.Summary, error)
	VolumeList(context.Context, volume.ListOptions) (volume.ListResponse, error)
	DiskUsage(context.Context, types.DiskUsageOptions) (types.DiskUsage, error)
}

// DialClient opens the local Docker API client the same way Compose
// execution does, so inventory and exec sessions always refer to the same
// daemon. Tests stand in with fakes against the narrow interfaces.
func DialClient() (*client.Client, func(), error) {
	c, err := client.NewClientWithOpts(client.WithHost("unix:///var/run/docker.sock"), client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, nil, err
	}
	return c, func() { _ = c.Close() }, nil
}

// Dial uses the same local socket as Compose execution, so the inventory
// and actions always refer to the same Docker daemon.
func Dial() (Docker, func(), error) {
	c, closeClient, err := DialClient()
	if err != nil {
		return nil, nil, err
	}
	return c, closeClient, nil
}

// Container is one entry of the container list shown in the console.
type Container struct {
	WritableBytes    *int64    `json:"writable_bytes,omitempty"`
	ConfigSampledAt  time.Time `json:"config_sampled_at,omitzero"`
	FullID           string    `json:"full_id,omitempty"`
	ImageID          string    `json:"image_id,omitempty"`
	Health           string    `json:"health,omitempty"`
	RestartPolicy    string    `json:"restart_policy,omitempty"`
	MemoryLimitBytes int64     `json:"memory_limit_bytes,omitempty"`
	PIDsLimit        int64     `json:"pids_limit,omitempty"`
	NanoCPUs         int64     `json:"nano_cpus,omitempty"`
	ComposeProject   string    `json:"compose_project,omitempty"`
	ComposeService   string    `json:"compose_service,omitempty"`
	Ports            []Port    `json:"ports,omitempty"`
	Mounts           []Mount   `json:"mounts,omitempty"`
	Networks         []string  `json:"networks,omitempty"`
	Stats            *Stats    `json:"stats,omitempty"`
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Image            string    `json:"image"`
	State            string    `json:"state"`
	Status           string    `json:"status"`
}

// Image is one entry of the image list.
type Image struct {
	ContainerCount *int     `json:"container_count,omitempty"`
	FullID         string   `json:"full_id,omitempty"`
	Created        int64    `json:"created,omitempty"`
	Digests        []string `json:"digests,omitempty"`
	ID             string   `json:"id"`
	Tags           []string `json:"tags,omitempty"`
	Size           int64    `json:"size"`
}

// Network is one entry of the network list.
type Network struct {
	ID             string   `json:"id,omitempty"`
	Subnets        []string `json:"subnets,omitempty"`
	Gateways       []string `json:"gateways,omitempty"`
	Name           string   `json:"name"`
	Driver         string   `json:"driver"`
	Scope          string   `json:"scope"`
	Internal       bool     `json:"internal"`
	Protected      bool     `json:"protected,omitempty"`
	ComposeProject string   `json:"compose_project,omitempty"`
}

// Volume is one entry of the volume list. Size is only known when the daemon
// reported usage data for it.
type Volume struct {
	Tmpfs          bool    `json:"tmpfs,omitempty"`
	CapacityBytes  int64   `json:"capacity_bytes,omitempty"`
	ComposeProject *string `json:"compose_project,omitempty"`
	Scope          string  `json:"scope,omitempty"`
	Mountpoint     string  `json:"mountpoint,omitempty"`
	RefCount       *int64  `json:"ref_count,omitempty"`
	Name           string  `json:"name"`
	Driver         string  `json:"driver"`
	Size           int64   `json:"size"`
	Known          bool    `json:"known"`
}

// DiskUsage summarizes "docker system df" in bytes.
type DiskUsage struct {
	Images     int64 `json:"images"`
	Containers int64 `json:"containers"`
	Volumes    int64 `json:"volumes"`
	BuildCache int64 `json:"build_cache"`
	Layers     int64 `json:"layers"`
}

// Filesystem is one real mount visible to the main service, with usage from
// statfs. Pseudo filesystems and duplicate mounts of the same device are
// omitted.
type Filesystem struct {
	UsageUnavailable bool    `json:"usage_unavailable,omitempty"`
	Mountpoint       string  `json:"mountpoint"`
	Device           string  `json:"device,omitempty"`
	Filesystem       string  `json:"filesystem,omitempty"`
	TotalBytes       uint64  `json:"total_bytes"`
	UsedBytes        uint64  `json:"used_bytes"`
	AvailBytes       uint64  `json:"avail_bytes"`
	UsedPercent      float64 `json:"used_percent"`
}

// Overview is the whole local machine snapshot the console renders. Lists
// are capped to keep the snapshot well under the transport frame limit;
// counts always reflect the true totals.
type Overview struct {
	Host            *Host                `json:"host,omitempty"`
	Engine          *Engine              `json:"engine,omitempty"`
	ComposeProjects []ComposeProject     `json:"compose_projects,omitempty"`
	SectionTimes    map[string]time.Time `json:"section_times,omitempty"`
	Available       bool                 `json:"available"`
	Message         string               `json:"message,omitempty"`
	Warning         string               `json:"warning,omitempty"`
	GeneratedAt     time.Time            `json:"generated_at,omitempty"`
	Truncated       bool                 `json:"truncated,omitempty"`
	ContainerCount  int                  `json:"container_count"`
	RunningCount    int                  `json:"running_count"`
	StoppedCount    int                  `json:"stopped_count"`
	ImageCount      int                  `json:"image_count"`
	NetworkCount    int                  `json:"network_count"`
	VolumeCount     int                  `json:"volume_count"`
	Containers      []Container          `json:"containers,omitempty"`
	Images          []Image              `json:"images,omitempty"`
	Networks        []Network            `json:"networks,omitempty"`
	Volumes         []Volume             `json:"volumes,omitempty"`
	DiskUsage       *DiskUsage           `json:"disk_usage,omitempty"`
	Filesystems     []Filesystem         `json:"filesystems,omitempty"`
}

// List caps keep one snapshot an order of magnitude below the agentwire
// frame limit even on busy daemons.
const (
	maxListedContainers = 500
	maxListedImages     = 500
	maxListedNetworks   = 200
	maxListedVolumes    = 200
)

// Inventory collects and caches local machine snapshots.
type Inventory struct {
	docker  Docker
	timeout time.Duration
	ttl     time.Duration

	refresh chan struct{}
	mu      sync.Mutex
	cache   *Overview
	fetched time.Time
	disk    diskState
}

// New returns an inventory reading through d. Startup should call Prefetch
// once so the first console visit is already warm.
func New(d Docker) *Inventory {
	return &Inventory{docker: d, refresh: make(chan struct{}, 1), timeout: 5 * time.Second, ttl: 10 * time.Second}
}

// Prefetch warms the cache; it is safe to run in a goroutine at startup.
func (i *Inventory) Prefetch(ctx context.Context) { i.Overview(ctx) }

// Overview refreshes once per TTL, even when multiple callers poll together.
func (i *Inventory) Overview(ctx context.Context) Overview {
	return i.overview(ctx, false)
}

// Refresh collects a new snapshot even when a mutation follows a recent sample.
func (i *Inventory) Refresh(ctx context.Context) Overview {
	return i.overview(ctx, true)
}

func (i *Inventory) overview(ctx context.Context, force bool) Overview {
	ctx, cancel := context.WithTimeout(ctx, i.timeout)
	defer cancel()
	select {
	case i.refresh <- struct{}{}:
		defer func() { <-i.refresh }()
	case <-ctx.Done():
		i.mu.Lock()
		defer i.mu.Unlock()
		if i.cache != nil {
			return *i.cache
		}
		return Overview{Message: "采集超时"}
	}
	i.mu.Lock()
	prev := i.cache
	fresh := prev != nil && time.Since(i.fetched) < i.ttl
	i.mu.Unlock()
	if fresh && !force {
		return *prev
	}
	o := refresh(ctx, i.docker, prev, &i.disk)
	i.mu.Lock()
	i.cache = &o
	i.fetched = time.Now()
	i.mu.Unlock()
	return o
}

func collect(ctx context.Context, d Docker) (Overview, error) {
	o := refresh(ctx, d, nil, &diskState{})
	if !o.Available {
		return o, errors.New("Docker inventory unavailable")
	}
	return o, nil
}

// diskState caches the daemon's disk usage between refreshes. "/system/df"
// walks every image layer and is by far the heaviest inventory read, while
// the sizes it reports move slowly, so a fresh snapshot is reused for a few
// minutes. Accessed only while holding the refresh slot, which serializes
// collection.
type diskState struct {
	usage types.DiskUsage
	at    time.Time
}

const diskReuseWindow = 5 * time.Minute

func (s *diskState) reuse(now time.Time) (types.DiskUsage, bool) {
	if !s.at.IsZero() && now.Sub(s.at) < diskReuseWindow && s.usage.Volumes != nil {
		return s.usage, true
	}
	return types.DiskUsage{}, false
}

func (s *diskState) keep(usage types.DiskUsage, at time.Time) {
	s.usage = usage
	s.at = at
}

func refresh(ctx context.Context, d Docker, prev *Overview, disk *diskState) Overview {
	o := Overview{}
	if prev != nil {
		o = *prev
	}
	o.SectionTimes = maps.Clone(o.SectionTimes)
	if o.SectionTimes == nil {
		o.SectionTimes = map[string]time.Time{}
	}
	now := time.Now().UTC()
	o.GeneratedAt = now
	o.Warning = ""
	o.Message = ""
	host, filesystems, fsOK, hostErr := sampleHost(now, o.Host)
	if hostErr == nil {
		o.Host = host
		o.SectionTimes["host"] = now
	} else {
		o.warn("主机信息读取失败：" + hostErr.Error())
	}
	if fsOK {
		o.Filesystems = filesystems
		o.SectionTimes["filesystems"] = now
	} else if o.Host != nil && o.Host.Scope != "process" {
		o.warn("文件系统信息不可用")
	}
	if d == nil {
		o.Message = "Docker 未连接"
		o.Bound()
		return o
	}

	// Each daemon endpoint can independently time out. Apply results in fixed
	// order after all readers finish, preserving timestamps of failed sections.
	type section struct {
		name string
		load func() (func(), error)
	}
	var usage types.DiskUsage
	var imageContainers map[string]int

	sections := []section{
		{"containers", func() (func(), error) {
			rows, err := d.ContainerList(ctx, container.ListOptions{All: true})
			if err != nil {
				return nil, err
			}
			items, running, projects, missing := containerRows(ctx, d, rows, o.Containers)
			return func() {
				imageContainers = make(map[string]int)
				for _, row := range rows {
					imageContainers[row.ImageID]++
				}
				o.Containers = items
				o.ContainerCount = len(rows)
				o.RunningCount = running
				o.StoppedCount = len(rows) - running
				o.ComposeProjects = projects
				if missing != "" {
					o.warn(missing)
				}
			}, nil
		}},
		{"images", func() (func(), error) {
			rows, err := d.ImageList(ctx, image.ListOptions{})
			if err != nil {
				return nil, err
			}
			return func() {
				o.Images = imageRows(rows)
				o.ImageCount = len(rows)
				if imageContainers != nil {
					for j := range o.Images {
						count := imageContainers[o.Images[j].FullID]
						o.Images[j].ContainerCount = &count
					}
				}
			}, nil
		}},
		{"networks", func() (func(), error) {
			rows, err := d.NetworkList(ctx, network.ListOptions{})
			if err != nil {
				return nil, err
			}
			return func() { o.Networks = networkRows(rows); o.NetworkCount = len(rows) }, nil
		}},
		{"disk_usage", func() (func(), error) {
			rows, cached := disk.reuse(now)
			if !cached {
				fresh, err := d.DiskUsage(ctx, types.DiskUsageOptions{})
				if err != nil {
					return nil, err
				}
				rows = fresh
				disk.keep(rows, now)
			}
			return func() {
				usage = rows
				o.DiskUsage = diskTotals(rows)
				if !o.SectionTimes["containers"].Equal(now) {
					o.Containers = slices.Clone(o.Containers)
				}
				sizes := make(map[string]int64, len(rows.Containers))
				for _, c := range rows.Containers {
					if c != nil && c.ID != "" && c.SizeRw >= 0 {
						sizes[c.ID] = c.SizeRw
					}
				}
				for j := range o.Containers {
					item := &o.Containers[j]
					item.WritableBytes = nil
					if size, ok := sizes[item.FullID]; ok {
						item.WritableBytes = &size
					}
				}
			}, nil
		}},
		{"volumes", func() (func(), error) {
			rows, err := d.VolumeList(ctx, volume.ListOptions{})
			if err != nil {
				return nil, err
			}
			return func() {
				o.Volumes = volumeRows(rows, usage.Volumes)
				o.VolumeCount = len(rows.Volumes)
			}, nil
		}},
	}
	if api, ok := d.(engineAPI); ok {
		sections = append(sections, section{"engine", func() (func(), error) {
			engine, err := readEngine(ctx, api, now)
			return func() { o.Engine = engine }, err
		}})
	}
	type result struct {
		apply func()
		err   error
	}
	results := make([]result, len(sections))
	var wg sync.WaitGroup
	for idx, s := range sections {
		wg.Go(func() { results[idx].apply, results[idx].err = s.load() })
	}
	wg.Wait()
	for idx, r := range results {
		name := sections[idx].name
		if r.err != nil {
			o.warn(name + " 读取失败")
			continue
		}
		r.apply()
		o.SectionTimes[name] = now
		o.Available = true
	}
	if !o.Available {
		o.Message = "无法读取本机 Docker 资源"
	}
	o.Bound()
	return o
}

func (o *Overview) warn(msg string) {
	if o.Warning != "" {
		o.Warning += "；"
	}
	o.Warning += msg
}
func clip[T any](items []T, max int) []T {
	if len(items) > max {
		return slices.Clone(items[:max])
	}
	return items
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
