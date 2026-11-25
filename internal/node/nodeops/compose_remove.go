package nodeops

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/errdefs"

	"github.com/maxtanforge/moori/internal/core/nodeops"
)

const composeProjectLabel = "com.docker.compose.project"

func (r Runner) removeCompose(ctx context.Context, req nodeops.Request) (nodeops.Result, error) {
	rows, err := r.Docker.ContainerList(ctx, container.ListOptions{All: true, Filters: filters.NewArgs(filters.Arg("label", composeProjectLabel+"="+req.Target))})
	if err != nil {
		return nodeops.Result{}, dockerError(err)
	}
	var containers []container.InspectResponse
	var mounts []container.MountPoint
	for _, row := range rows {
		if row.Labels[composeProjectLabel] != req.Target {
			continue
		}
		c, err := r.exactContainer(ctx, row.ID)
		if err != nil {
			return nodeops.Result{}, err
		}
		if c.Config == nil || c.Config.Labels[composeProjectLabel] != req.Target || c.State == nil {
			return nodeops.Result{}, errors.New("Compose 项目状态已变化，请刷新后重试")
		}
		containers = append(containers, c)
		mounts = append(mounts, c.Mounts...)
	}
	if len(containers) == 0 {
		return nodeops.Result{}, errors.New("Compose 项目已不存在，请刷新列表")
	}
	// Stop all services before removing any container. Do not force removal or
	// remove volumes implicitly, so partial failures preserve application data.
	for _, c := range containers {
		if c.State.Paused {
			if err := r.Docker.ContainerUnpause(ctx, c.ID); err != nil {
				return nodeops.Result{}, errors.Join(errors.New("Compose 停止未完成，未删除容器；请核查服务状态"), dockerMutationError(err))
			}
		}
		if c.State.Running || c.State.Restarting {
			timeout := 10
			if err := r.Docker.ContainerStop(ctx, c.ID, container.StopOptions{Timeout: &timeout}); err != nil {
				return nodeops.Result{}, errors.Join(errors.New("Compose 停止未完成，未删除容器；请核查服务状态"), dockerMutationError(err))
			}
		}
	}
	for _, c := range containers {
		if err := r.Docker.ContainerRemove(ctx, c.ID, container.RemoveOptions{}); err != nil {
			return nodeops.Result{}, errors.Join(errors.New("Compose 删除未完成，请刷新核查剩余容器"), dockerMutationError(err))
		}
	}
	if req.RemoveVolumes {
		if err := r.removeMountedVolumes(ctx, mounts, req.Target); err != nil {
			return nodeops.Result{}, errors.Join(errors.New("Compose 容器已删除，但部分挂载卷未能删除；请核查卷列表"), err)
		}
	}
	return nodeops.Result{
		Log:                fmt.Sprintf("已删除 Compose 项目 %s 的 %d 个容器，网络、源文件和镜像已保留。", req.Target, len(containers)),
		ResourceObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}, nil
}

func (r Runner) removeMountedVolumes(ctx context.Context, mounts []container.MountPoint, project string) error {
	var names []string
	for _, m := range mounts {
		if m.Type == "volume" && m.Name != "" && !slices.Contains(names, m.Name) {
			names = append(names, m.Name)
		}
	}
	var failures []error
	for _, name := range names {
		v, err := r.Docker.VolumeInspect(ctx, name)
		if errdefs.IsNotFound(err) {
			continue
		}
		if err != nil {
			failures = append(failures, errors.Join(fmt.Errorf("无法检查卷 %s", name), dockerError(err)))
			continue
		}
		if v.Name != name {
			failures = append(failures, fmt.Errorf("卷 %s 的标识发生变化，已保留", name))
			continue
		}
		if v.Scope != "local" || project != "" && v.Labels[composeProjectLabel] != project ||
			project == "" && v.Labels[composeProjectLabel] != "" {
			continue
		}
		if err := r.Docker.VolumeRemove(ctx, name, false); err != nil && !errdefs.IsNotFound(err) {
			failures = append(failures, errors.Join(fmt.Errorf("卷 %s 未删除", name), dockerMutationError(err)))
		}
	}
	return errors.Join(failures...)
}
