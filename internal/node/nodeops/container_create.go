package nodeops

import (
	"context"
	"errors"
	"maps"
	"slices"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
	"github.com/docker/go-connections/nat"

	"github.com/maxtanforge/moori/internal/core/nodeops"
	"github.com/maxtanforge/moori/internal/core/registry"
)

func (r Runner) createContainer(ctx context.Context, req nodeops.Request) (nodeops.Result, error) {
	edit := req.Edit
	config := &container.Config{Image: edit.Image}
	host := &container.HostConfig{}
	applyContainerResources(&host.Resources, edit.Resources)
	if edit.Restart != nil {
		name, retries, _ := nodeops.RestartPolicy(*edit.Restart)
		host.RestartPolicy = container.RestartPolicy{Name: container.RestartPolicyMode(name), MaximumRetryCount: retries}
	}
	if edit.Ports != nil {
		ports, bindings, err := nat.ParsePortSpecs(*edit.Ports)
		if err != nil {
			return nodeops.Result{}, errors.New("invalid Docker port mapping")
		}
		config.ExposedPorts, host.PortBindings = ports, bindings
	}
	for _, key := range slices.Sorted(maps.Keys(edit.Environment)) {
		value := key
		if edit.Environment[key] != nil {
			value += "=" + *edit.Environment[key]
		}
		config.Env = append(config.Env, value)
	}
	if len(edit.Labels) != 0 {
		config.Labels = make(map[string]string, len(edit.Labels))
		applyReplacementMap(config.Labels, edit.Labels)
	}
	if err := prepareVolumeMounts(host, nil, edit.Volumes); err != nil {
		return nodeops.Result{}, err
	}
	prepareBindMounts(host, edit.Binds)
	if err := r.ensureContainerImage(ctx, edit.Image); err != nil {
		return nodeops.Result{}, err
	}
	endpoints, err := r.resolveCreateResources(ctx, host, edit.Networks)
	if err != nil {
		return nodeops.Result{}, err
	}
	created, err := r.Docker.ContainerCreate(ctx, config, host, endpoints, nil, req.Name)
	if errdefs.IsConflict(err) {
		return nodeops.Result{}, errors.New("a container already uses this name or one of the requested resources")
	}
	if err != nil {
		return nodeops.Result{}, dockerMutationError(err)
	}
	if created.ID == "" {
		return nodeops.Result{}, nodeops.ErrOutcomeUnknown
	}
	if edit.Resources != nil && len(created.Warnings) > 0 {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := r.Docker.ContainerRemove(cleanup, created.ID, container.RemoveOptions{}); err != nil && !errdefs.IsNotFound(err) {
			return nodeops.Result{}, nodeops.ErrOutcomeUnknown
		}
		return nodeops.Result{}, errors.New("Docker reported a warning while configuring resource limits")
	}
	if err := r.Docker.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		startErr := dockerMutationError(err)
		if errors.Is(startErr, nodeops.ErrOutcomeUnknown) {
			return nodeops.Result{}, startErr
		}
		// Cleanup uses only the exact ID returned by this create call. A newly
		// running container, including one started elsewhere, cannot be removed.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		err = r.Docker.ContainerRemove(cleanup, created.ID, container.RemoveOptions{})
		if err != nil && !errdefs.IsNotFound(err) {
			return nodeops.Result{}, nodeops.ErrOutcomeUnknown
		}
		return nodeops.Result{}, startErr
	}
	return r.confirmedContainerResult(ctx, nodeops.Container{FullID: created.ID, Name: req.Name, Image: edit.Image}), nil
}

func (r Runner) ensureContainerImage(ctx context.Context, value string) error {
	if _, err := r.Docker.ImageInspect(ctx, value); !errdefs.IsNotFound(err) {
		return dockerError(err)
	}
	if !nodeops.PullableContainerImage(value) || r.PullImage == nil {
		return errors.New("image is unavailable locally; pull it on this node first")
	}
	if err := r.PullImage(ctx, value); err != nil {
		switch {
		case errors.Is(err, registry.ErrAmbiguous):
			return registry.ErrAmbiguous
		case errors.Is(err, registry.ErrSecureTransport):
			return registry.ErrSecureTransport
		case errors.Is(err, registry.ErrOperationCredentials):
			return registry.ErrOperationCredentials
		default:
			return errors.New("Docker could not pull the container image")
		}
	}
	return nil
}

func (r Runner) resolveCreateResources(ctx context.Context, host *container.HostConfig, networks *[]string) (*network.NetworkingConfig, error) {
	for _, item := range host.Mounts {
		if item.Type != mount.TypeVolume {
			continue
		}
		row, err := r.Docker.VolumeInspect(ctx, item.Source)
		if err != nil || row.Name != item.Source {
			return nil, errors.New("a selected named volume is unavailable; refresh the node's volumes")
		}
	}
	if networks == nil {
		return nil, nil
	}
	host.NetworkMode = "none"
	endpoints := &network.NetworkingConfig{EndpointsConfig: make(map[string]*network.EndpointSettings, len(*networks))}
	for _, name := range *networks {
		row, err := r.Docker.NetworkInspect(ctx, name, network.InspectOptions{})
		if err != nil || row.Name != name || row.ID == "" || row.Driver == "host" || row.Driver == "null" {
			return nil, errors.New("a selected network is unavailable; refresh the node's networks")
		}
		if host.NetworkMode == "none" {
			host.NetworkMode = container.NetworkMode(row.ID)
		}
		endpoints.EndpointsConfig[name] = &network.EndpointSettings{NetworkID: row.ID}
	}
	return endpoints, nil
}
