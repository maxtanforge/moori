// Package nodeops implements the bounded Docker management operations.
package nodeops

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/maxtanforge/moori/internal/core/nodeops"
)

type Docker interface {
	ContainerInspect(context.Context, string) (container.InspectResponse, error)
	ImageInspect(context.Context, string, ...client.ImageInspectOption) (image.InspectResponse, error)
	ImageTag(context.Context, string, string) error
	ImageRemove(context.Context, string, image.RemoveOptions) ([]image.DeleteResponse, error)
	ContainerList(context.Context, container.ListOptions) ([]container.Summary, error)
	ContainerLogs(context.Context, string, container.LogsOptions) (io.ReadCloser, error)
	ContainerStart(context.Context, string, container.StartOptions) error
	ContainerStop(context.Context, string, container.StopOptions) error
	ContainerRestart(context.Context, string, container.StopOptions) error
	ContainerCreate(context.Context, *container.Config, *container.HostConfig, *network.NetworkingConfig, *v1.Platform, string) (container.CreateResponse, error)
	ContainerRename(context.Context, string, string) error
	ContainerUpdate(context.Context, string, container.UpdateConfig) (container.UpdateResponse, error)
	ContainerPause(context.Context, string) error
	ContainerUnpause(context.Context, string) error
	ContainerRemove(context.Context, string, container.RemoveOptions) error
	VolumeInspect(context.Context, string) (volume.Volume, error)
	VolumeCreate(context.Context, volume.CreateOptions) (volume.Volume, error)
	VolumeRemove(context.Context, string, bool) error
	NetworkInspect(context.Context, string, network.InspectOptions) (network.Inspect, error)
	NetworkList(context.Context, network.ListOptions) ([]network.Summary, error)
	NetworkCreate(context.Context, string, network.CreateOptions) (network.CreateResponse, error)
	NetworkConnect(context.Context, string, string, *network.EndpointSettings) error
	NetworkDisconnect(context.Context, string, string, bool) error
	NetworkRemove(context.Context, string) error
}

type Runner struct {
	Docker    Docker
	PullImage func(context.Context, string) error
	client    *client.Client
}

// NewLocal shares the Docker socket used for Compose and inventory. Host and
// transport configuration from requests and the environment are ignored.
func NewLocal() (*Runner, error) {
	d, err := client.NewClientWithOpts(client.WithHost("unix:///var/run/docker.sock"), client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return &Runner{Docker: d, client: d}, nil
}

func (r *Runner) Close() error {
	if r.client != nil {
		return r.client.Close()
	}
	return nil
}

func (r Runner) Run(ctx context.Context, req nodeops.Request) (nodeops.Result, error) {
	if err := req.Validate(); err != nil {
		return nodeops.Result{}, err
	}
	if r.Docker == nil {
		return nodeops.Result{}, errors.New("Docker management is unavailable on this node")
	}
	switch req.Action {
	case nodeops.ContainerInspect, nodeops.ContainerLogs, nodeops.ContainerStart, nodeops.ContainerStop, nodeops.ContainerRestart,
		nodeops.ContainerPause, nodeops.ContainerResume, nodeops.ContainerRemove:
		return r.container(ctx, req)
	case nodeops.ContainerResourcesUpdate:
		return r.updateContainerResources(ctx, req)
	case nodeops.ContainerReplace:
		return r.replaceContainer(ctx, req, nil)
	case nodeops.ComposeRemove:
		return r.removeCompose(ctx, req)
	case nodeops.ContainerRelease:
		return r.releaseContainer(ctx, req)
	case nodeops.ImageTagPreview, nodeops.ImageTag:
		return r.tagImage(ctx, req)
	case nodeops.ImageDelete:
		return r.deleteImage(ctx, req)
	case nodeops.ContainerCreate:
		return r.createContainer(ctx, req)
	case nodeops.ContainerEditConfig:
		return r.containerEditConfig(ctx, req.Target)
	case nodeops.VolumeInspect, nodeops.VolumeCreate, nodeops.VolumeDelete:
		return r.volume(ctx, req)
	case nodeops.NetworkInspect, nodeops.NetworkCreate, nodeops.NetworkConnect, nodeops.NetworkDisconnect, nodeops.NetworkDelete:
		return r.network(ctx, req)
	default:
		return nodeops.Result{}, errors.New("operation is unavailable on this node")
	}
}

func dockerError(err error) error {
	switch {
	case err == nil, errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errdefs.IsNotFound(err):
		return errors.New("Docker resource was not found")
	case errdefs.IsConflict(err):
		return errors.New("Docker resource is in use or its state changed")
	case errdefs.IsForbidden(err), errdefs.IsUnauthorized(err):
		return errors.New("Docker denied the operation")
	case errdefs.IsInvalidParameter(err):
		return errors.New("Docker rejected the operation for this resource")
	default:
		return errors.New("Docker could not complete the operation")
	}
}

func dockerMutationError(err error) error {
	switch {
	case err == nil, errdefs.IsNotFound(err), errdefs.IsConflict(err), errdefs.IsForbidden(err),
		errdefs.IsUnauthorized(err), errdefs.IsInvalidParameter(err), errdefs.IsNotImplemented(err):
		return dockerError(err)
	default:
		return nodeops.ErrOutcomeUnknown
	}
}

// Keep reference lists stable while preserving the total for usage checks.
func trimReferences(rows []nodeops.Reference) []nodeops.Reference {
	slices.SortFunc(rows, func(a, b nodeops.Reference) int {
		if order := strings.Compare(a.Name, b.Name); order != 0 {
			return order
		}
		return strings.Compare(a.ID, b.ID)
	})
	return rows[:min(len(rows), 128)]
}

func referenceFrom(d container.Summary) nodeops.Reference {
	ref := nodeops.Reference{ID: d.ID, State: string(d.State)}
	if len(d.Names) != 0 {
		ref.Name = strings.TrimPrefix(d.Names[0], "/")
	}
	return ref
}

// CheckExistingCompose verifies the project against live Docker state.
func (r Runner) CheckExistingCompose(ctx context.Context, name string) error {
	_, err := r.existingComposeContainers(ctx, name)
	return err
}

// ComposeConfigFiles reads the source order reported by Docker Compose.
func (r Runner) ComposeConfigFiles(ctx context.Context, name string) ([]string, error) {
	rows, err := r.existingComposeContainers(ctx, name)
	if err != nil {
		return nil, err
	}
	const sourceLabel = "com.docker.compose.project.config_files"
	source := rows[0].Labels[sourceLabel]
	if source == "" {
		return nil, errors.New("容器没有记录 Compose 源文件路径")
	}
	for _, row := range rows[1:] {
		if row.Labels[sourceLabel] != source {
			return nil, errors.New("项目容器记录的 Compose 源文件不一致，请登记正确的文件顺序")
		}
	}
	return strings.Split(source, ","), nil
}

func (r Runner) existingComposeContainers(ctx context.Context, name string) ([]container.Summary, error) {
	rows, err := r.ComposeContainers(ctx, name)
	if err == nil && len(rows) == 0 {
		return nil, errors.New("no containers for this Compose project exist on the node")
	}
	return rows, err
}

// ComposeContainers lists all current project members, including stopped services.
func (r Runner) ComposeContainers(ctx context.Context, name string) ([]container.Summary, error) {
	if !nodeops.ValidExistingComposeName(name) {
		return nil, nodeops.ErrInvalid
	}
	rows, err := r.Docker.ContainerList(ctx, container.ListOptions{
		All: true, Filters: filters.NewArgs(filters.Arg("label", composeProjectLabel+"="+name)),
	})
	if err != nil {
		return nil, dockerError(err)
	}
	matching := make([]container.Summary, 0, len(rows))
	for _, item := range rows {
		if item.Labels[composeProjectLabel] == name {
			matching = append(matching, item)
		}
	}
	return matching, nil
}
