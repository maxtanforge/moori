package nodeops

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/errdefs"

	"github.com/maxtanforge/moori/internal/core/nodeops"
)

var errContainerGone = errors.New("Docker container no longer exists on this node")

func (r Runner) exactContainer(ctx context.Context, id string) (container.InspectResponse, error) {
	c, err := r.Docker.ContainerInspect(ctx, id)
	if errdefs.IsNotFound(err) {
		return c, errContainerGone
	}
	if err != nil {
		return c, dockerError(err)
	}
	if c.ContainerJSONBase == nil || c.ID != id {
		return c, errors.New("Docker container identity changed")
	}
	return c, nil
}

func (r Runner) container(ctx context.Context, req nodeops.Request) (nodeops.Result, error) {
	c, err := r.exactContainer(ctx, req.Target)
	if err != nil {
		return nodeops.Result{}, err
	}
	stopTimeout := 10
	stop := container.StopOptions{Timeout: &stopTimeout}
	switch req.Action {
	case nodeops.ContainerInspect:
		details := containerDetails(c)
		return nodeops.Result{Container: &details}, nil
	case nodeops.ContainerLogs:
		return r.logs(ctx, req, c)
	case nodeops.ContainerStart:
		err = r.Docker.ContainerStart(ctx, c.ID, container.StartOptions{})
	case nodeops.ContainerStop:
		err = r.Docker.ContainerStop(ctx, c.ID, stop)
	case nodeops.ContainerRestart:
		err = r.Docker.ContainerRestart(ctx, c.ID, stop)
	case nodeops.ContainerPause:
		err = r.Docker.ContainerPause(ctx, c.ID)
	case nodeops.ContainerResume:
		err = r.Docker.ContainerUnpause(ctx, c.ID)
	case nodeops.ContainerRemove:
		if c.Config == nil || c.Config.Labels["com.docker.compose.project"] != "" {
			return nodeops.Result{}, errors.New("remove Compose containers through their deployment")
		}
		if c.State == nil || c.State.Running || c.State.Restarting ||
			(c.State.Status != "created" && c.State.Status != "exited" && c.State.Status != "dead") {
			return nodeops.Result{}, errors.New("stop the container before removing it")
		}
		err = r.Docker.ContainerRemove(ctx, c.ID, container.RemoveOptions{})
		if err == nil && req.RemoveVolumes {
			if err := r.removeMountedVolumes(ctx, c.Mounts, ""); err != nil {
				return nodeops.Result{}, errors.Join(errors.New("容器已删除，但部分挂载卷未能删除；请刷新列表核查"), err)
			}
		}
		return observedRemoval(err)
	}
	if err == nil && (req.Action == nodeops.ContainerStart || req.Action == nodeops.ContainerStop ||
		req.Action == nodeops.ContainerRestart || req.Action == nodeops.ContainerPause || req.Action == nodeops.ContainerResume) {
		// Report the daemon's current state without reclassifying a completed
		// lifecycle change as failed if the follow-up inspection is unavailable.
		if current, inspectErr := r.exactContainer(ctx, c.ID); inspectErr == nil && current.State != nil {
			status := string(current.State.Status)
			result := nodeops.Container{
				FullID: c.ID, State: status, Status: status,
				StateObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
			}
			if status == "running" && current.State.Health != nil {
				result.Health = string(current.State.Health.Status)
			}
			return nodeops.Result{Container: &result}, nil
		}
	}
	return nodeops.Result{}, dockerMutationError(err)
}

func (r Runner) confirmedContainerResult(ctx context.Context, fallback nodeops.Container) nodeops.Result {
	result := nodeops.Result{Container: &fallback}
	read, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	current, err := r.exactContainer(read, fallback.FullID)
	if err == nil && current.State != nil {
		details := containerDetails(current)
		result.Container = &details
		result.ResourceObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	return result
}

func containerDetails(c container.InspectResponse) nodeops.Container {
	info := nodeops.Container{
		ID: c.ID[:12], FullID: c.ID, Name: strings.TrimPrefix(c.Name, "/"), ImageID: c.Image, RestartCount: c.RestartCount,
	}
	if c.Config != nil {
		info.Image = c.Config.Image
		info.ComposeProject = c.Config.Labels["com.docker.compose.project"]
		info.ComposeService = c.Config.Labels["com.docker.compose.service"]
	}
	if c.State != nil {
		info.State = string(c.State.Status)
		info.Status = info.State
		info.StartedAt, info.FinishedAt = c.State.StartedAt, c.State.FinishedAt
		if !c.State.Running {
			info.ExitCode = &c.State.ExitCode
		}
		if info.State == "running" && c.State.Health != nil {
			info.Health = string(c.State.Health.Status)
		}
	}
	if c.HostConfig != nil {
		info.RestartPolicy = string(c.HostConfig.RestartPolicy.Name)
		info.MemoryLimitBytes = c.HostConfig.Memory
		info.NanoCPUs, _, info.PIDsLimit = containerLimitValues(c.HostConfig.Resources)
	}
	if c.NetworkSettings != nil {
		info.Networks = slices.Sorted(maps.Keys(c.NetworkSettings.Networks))
		info.Networks = info.Networks[:min(len(info.Networks), 64)]
		for _, port := range slices.Sorted(maps.Keys(c.NetworkSettings.Ports)) {
			binds := c.NetworkSettings.Ports[port]
			item := nodeops.Port{Private: uint16(port.Int()), Protocol: port.Proto()}
			if len(binds) == 0 {
				info.Ports = append(info.Ports, item)
			}
			for _, binding := range binds[:min(len(binds), 64-len(info.Ports))] {
				n, _ := strconv.ParseUint(binding.HostPort, 10, 16)
				item.IP, item.Public = binding.HostIP, uint16(n)
				info.Ports = append(info.Ports, item)
			}
			if len(info.Ports) == 64 {
				break
			}
		}
	}
	for _, m := range c.Mounts[:min(len(c.Mounts), 64)] {
		info.Mounts = append(info.Mounts, nodeops.Mount{Type: string(m.Type), Name: m.Name, Source: m.Source, Destination: m.Destination, ReadOnly: !m.RW})
	}
	return info
}

func (r Runner) logs(ctx context.Context, req nodeops.Request, c container.InspectResponse) (nodeops.Result, error) {
	since, err := logRunStart(c, req.AllLogRuns)
	if err != nil {
		return nodeops.Result{}, err
	}
	tail := req.Tail
	if tail == 0 {
		tail = nodeops.DefaultLogLines
	}
	reader, err := r.Docker.ContainerLogs(ctx, req.Target, container.LogsOptions{ShowStdout: true, ShowStderr: true, Tail: strconv.Itoa(tail), Timestamps: req.Timestamps, Since: since})
	if err != nil {
		return nodeops.Result{}, dockerError(err)
	}
	defer reader.Close()
	data, err := readLogs(reader, c.Config != nil && c.Config.Tty)
	if err != nil {
		return nodeops.Result{}, dockerError(err)
	}
	return nodeops.Result{Log: string(data[:min(len(data), nodeops.MaxLogBytes)]), Truncated: len(data) > nodeops.MaxLogBytes}, nil
}

func readLogs(reader io.Reader, tty bool) ([]byte, error) {
	const limit = nodeops.MaxLogBytes + 1
	if tty {
		return io.ReadAll(io.LimitReader(reader, limit))
	}
	// Docker multiplexes stdout/stderr when TTY is off. Read only the
	// permitted part of each frame instead of allocating its advertised size.
	data := make([]byte, 0, limit)
	var frame [8]byte
	for len(data) < limit {
		if _, err := io.ReadFull(reader, frame[:]); err != nil {
			if errors.Is(err, io.EOF) {
				return data, nil
			}
			return nil, err
		}
		if frame[0] > 2 || frame[1] != 0 || frame[2] != 0 || frame[3] != 0 {
			return nil, errors.New("invalid Docker log stream")
		}
		n := int(min(uint32(limit-len(data)), binary.BigEndian.Uint32(frame[4:])))
		start := len(data)
		data = data[:start+n]
		if _, err := io.ReadFull(reader, data[start:]); err != nil {
			return nil, err
		}
	}
	return data, nil
}
