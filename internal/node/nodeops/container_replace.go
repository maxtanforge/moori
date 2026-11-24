package nodeops

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
	"github.com/docker/go-connections/nat"

	"github.com/maxtanforge/moori/internal/core/nodeops"
)

const (
	replacementRecoveryTimeout = 25 * time.Second
	replacementMutationMargin  = 10 * time.Second
	replacementCleanupMargin   = 5 * time.Second
)

func (r Runner) replaceContainer(ctx context.Context, req nodeops.Request, expected *container.InspectResponse) (nodeops.Result, error) {
	operation, cancelOperation := context.WithTimeout(ctx, req.Action.Timeout())
	defer cancelOperation()
	deadline, _ := operation.Deadline()
	workDeadline := deadline.Add(-replacementRecoveryTimeout)
	ctx, cancelWork := context.WithDeadline(operation, workDeadline)
	defer cancelWork()

	old, err := r.exactContainer(ctx, req.Target)
	if err != nil {
		return nodeops.Result{}, err
	}
	if expected != nil && (old.Name != expected.Name || old.Image != expected.Image ||
		old.Config == nil || old.Config.Image != expected.Config.Image) {
		return nodeops.Result{}, errors.New("the webhook container changed while preparing the update")
	}
	config, host, endpoints, err := prepareReplacement(old, *req.Edit)
	if err != nil {
		return nodeops.Result{}, err
	}
	name := strings.TrimPrefix(old.Name, "/")
	if name == "" {
		return nodeops.Result{}, errors.New("container has no stable name")
	}
	// Ordinary edits can select a new image that is not cached yet. The CI
	// release path already verifies the repository and pulls its pinned image.
	if req.Edit.Image != "" && expected == nil {
		if err := r.ensureContainerImage(ctx, config.Image); err != nil {
			return nodeops.Result{}, err
		}
	}
	if old.State.Running {
		if err := checkReplacementBudget(old.Config.StopTimeout, config.Healthcheck, time.Until(workDeadline)); err != nil {
			return nodeops.Result{}, err
		}
	}
	stage, err := r.Docker.ContainerCreate(ctx, config, host, endpoints, nil, "moori-stage-"+strings.ToLower(rand.Text()))
	if errdefs.IsNotFound(err) {
		return nodeops.Result{}, errors.New("Docker could not find the replacement image or an attached resource")
	}
	if err != nil {
		return nodeops.Result{}, dockerMutationError(err)
	}
	if stage.ID == "" {
		return nodeops.Result{}, nodeops.ErrOutcomeUnknown
	}
	// Rollback can outlive a cancelled forward operation, within its reserved budget.
	recoveryContext := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.WithoutCancel(ctx), min(time.Until(deadline), replacementRecoveryTimeout))
	}
	discardStage := func(cause error) (nodeops.Result, error) {
		recovery, done := recoveryContext()
		defer done()
		if err := r.Docker.ContainerRemove(recovery, stage.ID, container.RemoveOptions{}); err != nil && !errdefs.IsNotFound(err) {
			return nodeops.Result{}, nodeops.ErrOutcomeUnknown
		}
		return nodeops.Result{}, cause
	}
	if req.Edit.Resources != nil && len(stage.Warnings) > 0 {
		return discardStage(errors.New("Docker reported a warning while configuring resource limits"))
	}
	restore := func() (nodeops.Result, error) {
		recovery, done := recoveryContext()
		defer done()
		if err := r.restoreOriginal(recovery, old.ID, stage.ID, name, old.State.Running); err != nil {
			return nodeops.Result{}, nodeops.ErrOutcomeUnknown
		}
		return nodeops.Result{}, errors.New("Docker could not apply the replacement. The original container was restored.")
	}
	var expectsHealth bool
	if old.State.Running {
		// A different image can add a health check when Docker merges image defaults.
		staged, err := r.exactContainer(ctx, stage.ID)
		if errors.Is(err, errContainerGone) {
			return discardStage(errors.New("Docker staged replacement disappeared before the original was stopped"))
		}
		if err != nil {
			return discardStage(err)
		}
		if staged.Config == nil {
			return discardStage(errors.New("Docker did not return the replacement container configuration"))
		}
		if err := checkReplacementBudget(old.Config.StopTimeout, staged.Config.Healthcheck, time.Until(workDeadline)); err != nil {
			return discardStage(err)
		}
		expectsHealth = hasReplacementHealthCheck(staged.Config.Healthcheck)
		if err := r.Docker.ContainerStop(ctx, old.ID, container.StopOptions{}); err != nil {
			if errors.Is(dockerMutationError(err), nodeops.ErrOutcomeUnknown) {
				// Docker can continue stopping the original after the HTTP reply is lost.
				return discardStage(nodeops.ErrOutcomeUnknown)
			}
			return restore()
		}
	}
	if err := r.Docker.ContainerRename(ctx, old.ID, "moori-previous-"+strings.ToLower(rand.Text())); err != nil {
		return restore()
	}
	if err := r.Docker.ContainerRename(ctx, stage.ID, name); err != nil {
		return restore()
	}
	if old.State.Running {
		if err := r.Docker.ContainerStart(ctx, stage.ID, container.StartOptions{}); err != nil {
			return restore()
		}
		verify, finishVerify := context.WithDeadline(ctx, workDeadline.Add(-replacementCleanupMargin))
		err := r.verifyReplacement(verify, stage.ID, expectsHealth)
		finishVerify()
		if err != nil {
			return restore()
		}
	}
	// Never remove any volume. A daemon failure at this point must not
	// cause a second container to start while the replacement may be live.
	if err := r.Docker.ContainerRemove(ctx, old.ID, container.RemoveOptions{}); err != nil {
		return nodeops.Result{}, nodeops.ErrOutcomeUnknown
	}
	replacement := nodeops.Container{FullID: stage.ID, Name: name, Image: config.Image}
	return r.confirmedContainerResult(ctx, replacement), nil
}

func hasReplacementHealthCheck(config *container.HealthConfig) bool {
	return config != nil && len(config.Test) != 0 && config.Test[0] != "NONE"
}

func checkReplacementBudget(stopTimeout *int, check *container.HealthConfig, remaining time.Duration) error {
	stopGrace := 10 * time.Second // Docker's Linux default when StopTimeout is unset.
	if stopTimeout != nil {
		seconds := *stopTimeout
		if seconds < 0 || seconds > int(remaining/time.Second) {
			return errors.New("the container's stop timeout exceeds the safe replacement budget")
		}
		stopGrace = time.Duration(seconds) * time.Second
	}
	var healthWindow time.Duration
	if hasReplacementHealthCheck(check) {
		interval, timeout, startInterval := check.Interval, check.Timeout, check.StartInterval
		if interval == 0 {
			interval = 30 * time.Second
		}
		if timeout == 0 {
			timeout = 30 * time.Second
		}
		if startInterval == 0 {
			startInterval = 5 * time.Second
		}
		if min(interval, timeout, startInterval, check.StartPeriod) < 0 ||
			max(interval, timeout, startInterval, check.StartPeriod) > remaining {
			return errors.New("the replacement's health check exceeds the safe replacement budget")
		}
		healthWindow = interval + timeout
		if check.StartPeriod != 0 {
			// A starting probe can still be running when Docker's grace period ends.
			healthWindow = check.StartPeriod + max(interval+2*timeout, startInterval+timeout)
		}
	}
	if stopGrace+healthWindow+replacementMutationMargin+replacementCleanupMargin > remaining {
		return errors.New("the container's stop and health settings leave insufficient time for a safe replacement")
	}
	return nil
}

func (r Runner) verifyReplacement(ctx context.Context, id string, expectsHealth bool) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		item, err := r.exactContainer(ctx, id)
		if err != nil {
			return err
		}
		if item.State == nil || !item.State.Running || item.State.Restarting {
			return errors.New("replacement is not running")
		}
		if health := item.State.Health; health != nil {
			switch health.Status {
			case container.Healthy:
				return nil
			case container.Unhealthy:
				return errors.New("replacement health check failed")
			}
		} else if !expectsHealth {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (r Runner) restoreOriginal(ctx context.Context, original, candidate, name string, wasRunning bool) error {
	if err := r.Docker.ContainerRemove(ctx, candidate, container.RemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
		return err
	}
	item, err := r.exactContainer(ctx, original)
	if err != nil || item.State == nil {
		return nodeops.ErrOutcomeUnknown
	}
	if strings.TrimPrefix(item.Name, "/") != name {
		if err := r.Docker.ContainerRename(ctx, original, name); err != nil {
			return err
		}
	}
	if wasRunning && !item.State.Running {
		return r.Docker.ContainerStart(ctx, original, container.StartOptions{})
	}
	return nil
}

func prepareReplacement(old container.InspectResponse, edit nodeops.ContainerEdit) (*container.Config, *container.HostConfig, *network.NetworkingConfig, error) {
	if old.Config == nil || old.HostConfig == nil || old.State == nil || old.Image == "" {
		return nil, nil, nil, errors.New("Docker did not return the original container configuration")
	}
	if old.Config.Labels["com.docker.compose.project"] != "" || old.Config.Labels["com.docker.swarm.service.id"] != "" {
		return nil, nil, nil, errors.New("edit this container through its Compose project or service")
	}
	if old.State.Paused || old.State.Restarting || old.State.Dead ||
		old.HostConfig.AutoRemove || old.HostConfig.ContainerIDFile != "" || len(old.HostConfig.VolumesFrom) != 0 ||
		old.HostConfig.NetworkMode.IsContainer() || old.HostConfig.NetworkMode.IsHost() {
		return nil, nil, nil, errors.New("this container uses a state or runtime mode that cannot be replaced safely")
	}
	config, host := *old.Config, *old.HostConfig
	applyContainerResources(&host.Resources, edit.Resources)
	config.Image = old.Image
	if edit.Image != "" {
		config.Image = edit.Image
	}
	if edit.Restart != nil {
		name, retries, _ := nodeops.RestartPolicy(*edit.Restart)
		host.RestartPolicy = container.RestartPolicy{Name: container.RestartPolicyMode(name), MaximumRetryCount: retries}
	}
	if edit.Ports != nil {
		ports, bindings, err := nat.ParsePortSpecs(*edit.Ports)
		if err != nil {
			return nil, nil, nil, errors.New("invalid Docker port mapping")
		}
		config.ExposedPorts, host.PortBindings = ports, bindings
		host.PublishAllPorts = false
	}
	if len(edit.Environment) > 0 {
		env := make(map[string]string)
		for _, pair := range config.Env {
			key, _, _ := strings.Cut(pair, "=")
			env[key] = pair
		}
		for key, value := range edit.Environment {
			// A key without '=' explicitly unsets an inherited image default.
			env[key] = key
			if value != nil {
				env[key] += "=" + *value
			}
		}
		config.Env = make([]string, 0, len(env))
		for _, key := range slices.Sorted(maps.Keys(env)) {
			config.Env = append(config.Env, env[key])
		}
	}
	if len(edit.Labels) > 0 {
		config.Labels = make(map[string]string, len(config.Labels)+len(edit.Labels))
		maps.Copy(config.Labels, old.Config.Labels)
		applyReplacementMap(config.Labels, edit.Labels)
	}
	if err := prepareVolumeMounts(&host, old.Mounts, edit.Volumes); err != nil {
		return nil, nil, nil, err
	}
	prepareBindMounts(&host, edit.Binds)
	if (edit.Volumes != nil || edit.Binds != nil) && duplicateMountTargets(&host) {
		return nil, nil, nil, errors.New("multiple mounts use the same container path")
	}
	endpoints := &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{}}
	if old.NetworkSettings != nil {
		for name, existing := range old.NetworkSettings.Networks {
			if existing == nil || name == "none" {
				continue
			}
			if existing.IPAMConfig != nil && (existing.IPAMConfig.IPv4Address != "" ||
				existing.IPAMConfig.IPv6Address != "" || len(existing.IPAMConfig.LinkLocalIPs) != 0) {
				return nil, nil, nil, errors.New("container has a fixed network address that requires manual migration")
			}
			endpoints.EndpointsConfig[name] = &network.EndpointSettings{Aliases: existing.Aliases, Links: existing.Links,
				DriverOpts: existing.DriverOpts, GwPriority: existing.GwPriority}
		}
	}
	if edit.Networks != nil {
		chosen := make(map[string]*network.EndpointSettings)
		host.NetworkMode = "none"
		for _, name := range *edit.Networks {
			chosen[name] = endpoints.EndpointsConfig[name]
			if chosen[name] == nil {
				chosen[name] = &network.EndpointSettings{}
			}
		}
		if len(*edit.Networks) != 0 {
			host.NetworkMode = container.NetworkMode((*edit.Networks)[0])
		}
		endpoints.EndpointsConfig = chosen
	}
	return &config, &host, endpoints, nil
}

func prepareVolumeMounts(host *container.HostConfig, current []container.MountPoint, desired *[]string) error {
	volumeByTarget := make(map[string]container.MountPoint)
	for _, item := range current {
		if item.Type == mount.TypeVolume {
			volumeByTarget[item.Destination] = item
		}
	}
	var edits []mount.Mount
	editByTarget := make(map[string]int)
	if desired != nil {
		for _, item := range *desired {
			parts := strings.Split(item, ":")
			editByTarget[parts[1]] = len(edits)
			edits = append(edits, mount.Mount{
				Type: mount.TypeVolume, Source: parts[0], Target: parts[1],
				ReadOnly: len(parts) == 3 && parts[2] == "ro",
			})
		}
	}
	binds := make([]string, 0, len(host.Binds))
	mounts := make([]mount.Mount, 0, len(host.Mounts)+len(edits))
	for _, bind := range host.Binds {
		parts := strings.SplitN(bind, ":", 3)
		if len(parts) > 1 && path.IsAbs(parts[0]) {
			binds = append(binds, bind)
			continue
		}
		if len(parts) == 1 {
			target := path.Clean(parts[0])
			source := volumeByTarget[target].Name
			if desired != nil && source == "" {
				continue
			}
			if source == "" {
				return fmt.Errorf("Docker did not report the existing volume for %s", target)
			}
			parts = []string{source, target}
		}
		source, target := parts[0], path.Clean(parts[1])
		delete(volumeByTarget, target)
		if desired != nil {
			index, ok := editByTarget[target]
			if !ok || edits[index].Source != source {
				continue
			}
			parts = retainVolumeBindMode(parts, edits[index].ReadOnly)
			edits[index].Target = "" // Already carried forward in HostConfig.Binds.
		}
		binds = append(binds, strings.Join(parts, ":"))
	}
	for _, existing := range host.Mounts {
		if existing.Type != mount.TypeVolume {
			mounts = append(mounts, existing)
			continue
		}
		target := path.Clean(existing.Target)
		observed := volumeByTarget[target]
		delete(volumeByTarget, target)
		if existing.Source == "" {
			existing.Source = observed.Name
		}
		if desired != nil {
			index, ok := editByTarget[target]
			if !ok || edits[index].Source != existing.Source {
				continue
			}
			existing.ReadOnly = edits[index].ReadOnly
			edits[index].Target = "" // Preserve advanced options on this structured mount.
		}
		if existing.Source == "" {
			return fmt.Errorf("Docker did not report the existing volume for %s", existing.Target)
		}
		mounts = append(mounts, existing)
	}
	if desired == nil {
		// Image-declared volumes appear only in the inspected mount points.
		for _, target := range slices.Sorted(maps.Keys(volumeByTarget)) {
			old := volumeByTarget[target]
			if old.Name == "" {
				return fmt.Errorf("Docker did not report the existing volume for %s", target)
			}
			mounts = append(mounts, mount.Mount{Type: mount.TypeVolume, Source: old.Name, Target: target, ReadOnly: !old.RW})
		}
	}
	for _, item := range edits {
		if item.Target != "" {
			mounts = append(mounts, item)
		}
	}
	host.Binds, host.Mounts = binds, mounts
	return nil
}

func retainVolumeBindMode(parts []string, readOnly bool) []string {
	var flags []string
	if len(parts) == 3 {
		flags = strings.Split(parts[2], ",")
	}
	if slices.Contains(flags, "ro") == readOnly {
		return parts
	}
	flags = slices.DeleteFunc(flags, func(flag string) bool { return flag == "ro" || flag == "rw" })
	if readOnly {
		flags = append(flags, "ro")
	}
	if len(flags) == 0 {
		return parts[:2]
	}
	return append(parts[:2], strings.Join(flags, ","))
}

func applyReplacementMap(base map[string]string, patch map[string]*string) {
	for key, value := range patch {
		if value == nil {
			delete(base, key)
		} else {
			base[key] = *value
		}
	}
}
