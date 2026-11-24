package localinv

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/system"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"

	"github.com/maxtanforge/moori/internal/core/nodeops"
)

type engineAPI interface {
	Info(context.Context) (system.Info, error)
	ServerVersion(context.Context) (types.Version, error)
}

func readEngine(ctx context.Context, d engineAPI, at time.Time) (*Engine, error) {
	info, err := d.Info(ctx)
	if err != nil {
		return nil, err
	}
	version, err := d.ServerVersion(ctx)
	if err != nil {
		return nil, err
	}
	return &Engine{SampledAt: at, Name: info.Name, Version: version.Version, APIVersion: version.APIVersion, RootDir: info.DockerRootDir, StorageDriver: info.Driver, LoggingDriver: info.LoggingDriver, CgroupDriver: info.CgroupDriver, CgroupVersion: info.CgroupVersion, DefaultRuntime: info.DefaultRuntime}, nil
}

func imageRows(rows []image.Summary) []Image {
	out := make([]Image, 0, len(rows))
	for _, row := range rows {
		var tags []string
		for _, t := range row.RepoTags {
			if t != "<none>:<none>" {
				tags = append(tags, t)
			}
		}
		sort.Strings(tags)
		out = append(out, Image{ID: shortID(row.ID), FullID: row.ID, Tags: tags, Digests: row.RepoDigests, Created: row.Created, Size: row.Size})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Size == out[j].Size {
			return out[i].FullID < out[j].FullID
		}
		return out[i].Size > out[j].Size
	})
	return clip(out, maxListedImages)
}
func networkRows(rows []network.Summary) []Network {
	var out []Network
	for _, r := range rows {
		n := Network{Name: r.Name, ID: r.ID, Driver: r.Driver, Scope: r.Scope, Internal: r.Internal,
			Protected:      nodeops.ProtectedNetwork(r.Name, r.Scope, r.Driver, r.Ingress, r.ConfigOnly, r.Options),
			ComposeProject: r.Labels["com.docker.compose.project"]}
		for _, ip := range r.IPAM.Config {
			if ip.Subnet != "" {
				n.Subnets = append(n.Subnets, ip.Subnet)
			}
			if ip.Gateway != "" {
				n.Gateways = append(n.Gateways, ip.Gateway)
			}
		}
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			return out[i].ID < out[j].ID
		}
		return out[i].Name < out[j].Name
	})
	return clip(out, maxListedNetworks)
}
func volumeRows(rows volume.ListResponse, usage []*volume.Volume) []Volume {
	sizes := map[string]*volume.UsageData{}
	for _, v := range usage {
		if v != nil && v.UsageData != nil {
			sizes[v.Name] = v.UsageData
		}
	}
	var out []Volume
	for _, v := range rows.Volumes {
		if v == nil {
			continue
		}
		project := v.Labels["com.docker.compose.project"]
		tmpfs, capacity := nodeops.TmpfsCapacity(v.Driver, v.Options)
		item := Volume{Tmpfs: tmpfs, CapacityBytes: capacity, Name: v.Name, Driver: v.Driver, Scope: v.Scope, Mountpoint: v.Mountpoint, ComposeProject: &project}
		u := sizes[v.Name]
		if u == nil {
			u = v.UsageData
		}
		if u != nil {
			if u.Size >= 0 {
				item.Size = u.Size
				item.Known = true
			}
			if u.RefCount >= 0 {
				item.RefCount = &u.RefCount
			}
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return clip(out, maxListedVolumes)
}
func diskTotals(usage types.DiskUsage) *DiskUsage {
	o := &DiskUsage{Layers: usage.LayersSize}
	for _, v := range usage.Containers {
		if v != nil && v.SizeRw >= 0 {
			o.Containers += v.SizeRw
		}
	}
	for _, v := range usage.Images {
		if v != nil && v.Size >= 0 {
			o.Images += v.Size
		}
	}
	for _, v := range usage.Volumes {
		if v != nil && v.UsageData != nil && v.UsageData.Size >= 0 {
			o.Volumes += v.UsageData.Size
		}
	}
	for _, v := range usage.BuildCache {
		if v != nil && v.Size >= 0 {
			o.BuildCache += v.Size
		}
	}
	return o
}

var _ engineAPI = (*client.Client)(nil)
var _ detailAPI = (*client.Client)(nil)

type detailAPI interface {
	ContainerInspect(context.Context, string) (container.InspectResponse, error)
	ContainerStatsOneShot(context.Context, string) (container.StatsResponseReader, error)
}

func containerRows(ctx context.Context, d Docker, rows []container.Summary, previous []Container) ([]Container, int, []ComposeProject, string) {
	services := map[string]map[string]bool{}
	running := 0
	out := make([]Container, 0, len(rows))
	for _, r := range rows {
		c := Container{ID: shortID(r.ID), FullID: r.ID, Image: r.Image, ImageID: r.ImageID, State: string(r.State), Status: r.Status, ComposeProject: r.Labels["com.docker.compose.project"], ComposeService: r.Labels["com.docker.compose.service"]}
		if len(r.Names) > 0 {
			c.Name = strings.TrimPrefix(r.Names[0], "/")
		}
		if c.State == "running" {
			running++
		}
		if c.ComposeProject != "" && c.ComposeService != "" {
			if services[c.ComposeProject] == nil {
				services[c.ComposeProject] = map[string]bool{}
			}
			services[c.ComposeProject][c.ComposeService] = true
		}
		for _, p := range r.Ports {
			c.Ports = append(c.Ports, Port{IP: p.IP, Private: p.PrivatePort, Public: p.PublicPort, Protocol: p.Type})
		}
		for _, m := range r.Mounts {
			c.Mounts = append(c.Mounts, Mount{Type: string(m.Type), Name: m.Name, Source: m.Source, Destination: m.Destination, ReadOnly: !m.RW})
		}
		if r.NetworkSettings != nil {
			c.Networks = slices.Sorted(maps.Keys(r.NetworkSettings.Networks))
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			return out[i].FullID < out[j].FullID
		}
		return out[i].Name < out[j].Name
	})
	out = clip(out, maxListedContainers)
	var projects []ComposeProject
	for _, name := range slices.Sorted(maps.Keys(services)) {
		projects = append(projects, ComposeProject{Name: name, Services: slices.Sorted(maps.Keys(services[name]))})
	}
	warning := ""
	if api, ok := d.(detailAPI); ok {
		old := map[string]Container{}
		for _, r := range previous {
			old[r.FullID] = r
		}
		for idx := range out {
			c := &out[idx]
			prev := old[c.FullID]
			c.WritableBytes = prev.WritableBytes
			c.ConfigSampledAt = prev.ConfigSampledAt
			c.RestartPolicy = prev.RestartPolicy
			c.NanoCPUs = prev.NanoCPUs
			c.PIDsLimit = prev.PIDsLimit
			c.MemoryLimitBytes = prev.MemoryLimitBytes
			if c.State == "running" {
				if prev.State == "running" {
					c.Health = prev.Health
				}
				c.Stats = prev.Stats
			}
		}
		var misses atomic.Int64
		var wg sync.WaitGroup
		jobs := make(chan int)
		for range min(6, len(out)) {
			wg.Go(func() {
				for idx := range jobs {
					c := &out[idx]
					if !enrich(ctx, api, c, c.Stats) {
						misses.Add(1)
					}
				}
			})
		}
	dispatch:
		for idx := range out {
			select {
			case jobs <- idx:
			case <-ctx.Done():
				misses.Add(int64(len(out) - idx))
				break dispatch
			}
		}
		close(jobs)
		wg.Wait()
		if n := misses.Load(); n > 0 {
			warning = fmt.Sprintf("%d 个容器的详细配置或实时统计未更新", n)
		}
	}
	return out, running, projects, warning
}
func enrich(ctx context.Context, api detailAPI, c *Container, old *Stats) bool {
	info, err := api.ContainerInspect(ctx, c.FullID)
	if err == nil && info.ContainerJSONBase != nil {
		c.ConfigSampledAt = time.Now().UTC()
		c.RestartPolicy = ""
		c.MemoryLimitBytes = 0
		c.NanoCPUs = 0
		c.PIDsLimit = 0
		c.Health = ""
		if info.HostConfig != nil {
			h := info.HostConfig
			c.RestartPolicy = string(h.RestartPolicy.Name)
			c.MemoryLimitBytes = h.Memory
			c.NanoCPUs = h.NanoCPUs
			if h.PidsLimit != nil {
				c.PIDsLimit = max(0, *h.PidsLimit)
			}
		}
		if c.State == "running" && info.State != nil && string(info.State.Status) == "running" && info.State.Health != nil {
			c.Health = string(info.State.Health.Status)
		}
	}
	if c.State != "running" {
		return err == nil
	}
	c.Stats = old
	reader, e := api.ContainerStatsOneShot(ctx, c.FullID)
	if e != nil {
		return false
	}
	defer reader.Body.Close()
	var raw container.StatsResponse
	if json.NewDecoder(io.LimitReader(reader.Body, 2<<20)).Decode(&raw) != nil || raw.Read.IsZero() {
		return false
	}
	// The one-shot endpoint usually omits the previous CPU measurement.
	if raw.PreRead.IsZero() && old != nil && old.SampledAt.Before(raw.Read) {
		raw.PreRead = old.SampledAt
		raw.PreCPUStats.SystemUsage = old.systemCPU
		raw.PreCPUStats.CPUUsage.TotalUsage = old.totalCPU
	}
	c.Stats = statsFrom(raw)
	return err == nil
}
func statsFrom(raw container.StatsResponse) *Stats {
	cpu, pre := raw.CPUStats, raw.PreCPUStats
	s := &Stats{SampledAt: raw.Read, totalCPU: cpu.CPUUsage.TotalUsage, systemCPU: cpu.SystemUsage, MemoryBytes: raw.MemoryStats.Usage, MemoryLimitBytes: raw.MemoryStats.Limit, PIDs: raw.PidsStats.Current, BlockReadBytes: raw.StorageStats.ReadSizeBytes, BlockWriteBytes: raw.StorageStats.WriteSizeBytes}
	cores := cpu.OnlineCPUs
	if cores == 0 {
		cores = uint32(len(cpu.CPUUsage.PercpuUsage))
	}
	if !raw.PreRead.IsZero() && cores > 0 && cpu.SystemUsage > pre.SystemUsage && cpu.CPUUsage.TotalUsage >= pre.CPUUsage.TotalUsage {
		pct := float64(cpu.CPUUsage.TotalUsage-pre.CPUUsage.TotalUsage) / float64(cpu.SystemUsage-pre.SystemUsage) * float64(cores) * 100
		s.CPUPercent = &pct
	}
	cache, ok := raw.MemoryStats.Stats["total_inactive_file"]
	if !ok {
		cache = raw.MemoryStats.Stats["inactive_file"]
	}
	if cache <= s.MemoryBytes {
		s.MemoryBytes -= cache
	}
	if raw.MemoryStats.PrivateWorkingSet > 0 {
		s.MemoryBytes = raw.MemoryStats.PrivateWorkingSet
	}
	for _, n := range raw.Networks {
		s.RxBytes += n.RxBytes
		s.TxBytes += n.TxBytes
	}
	for _, v := range raw.BlkioStats.IoServiceBytesRecursive {
		switch strings.ToLower(v.Op) {
		case "read":
			s.BlockReadBytes += v.Value
		case "write":
			s.BlockWriteBytes += v.Value
		}
	}
	return s
}
