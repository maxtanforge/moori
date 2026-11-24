package nodeops

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/maxtanforge/moori/internal/core/nodeops"
)

type replacingDocker struct {
	Docker
	oldID, newID string
	oldName      string
	running      bool
	failStart    bool
	steps        []string
	createConfig *container.Config
	createHost   *container.HostConfig
	stopTimeout  *int
	stopOptions  container.StopOptions
	health       *container.HealthConfig
	imageHealth  *container.HealthConfig
	stopError    error
	healthChecks int
	healthWait   time.Duration
	imageMissing bool
	oldMissing   bool
}

func (d *replacingDocker) ImageInspect(context.Context, string, ...client.ImageInspectOption) (image.InspectResponse, error) {
	if d.imageMissing {
		return image.InspectResponse{}, errdefs.NotFound(errors.New("image not found"))
	}
	return image.InspectResponse{}, nil
}

func (d *replacingDocker) ContainerInspect(ctx context.Context, id string) (container.InspectResponse, error) {
	if id == d.oldID && d.oldMissing {
		return container.InspectResponse{}, errdefs.NotFound(errors.New("container disappeared"))
	}
	if id == d.newID {
		state := &container.State{Running: slices.Contains(d.steps, "start-new")}
		if state.Running && d.health != nil {
			d.healthChecks++
			if deadline, ok := ctx.Deadline(); ok && !slices.Contains(d.steps, "remove-old") {
				d.healthWait = time.Until(deadline)
			}
			state.Health = &container.Health{Status: container.Starting}
			if d.healthChecks > 1 {
				state.Health.Status = container.Healthy
			}
		}
		return container.InspectResponse{
			ContainerJSONBase: &container.ContainerJSONBase{ID: id, State: state},
			Config:            d.createConfig,
		}, nil
	}
	return container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{ID: id, Name: "/" + d.oldName, Image: "sha256:" + strings.Repeat("c", 64),
			State: &container.State{Running: d.running}, HostConfig: &container.HostConfig{}},
		Config: &container.Config{Env: []string{"TOKEN=keep-only-on-node", "MODE=old"}, StopTimeout: d.stopTimeout, Healthcheck: d.health},
		Mounts: []container.MountPoint{{Type: mount.TypeVolume, Name: "app-data", Destination: "/data", RW: true}},
	}, nil
}
func (d *replacingDocker) ContainerCreate(_ context.Context, cfg *container.Config, host *container.HostConfig, _ *network.NetworkingConfig, _ *v1.Platform, _ string) (container.CreateResponse, error) {
	d.steps = append(d.steps, "create")
	d.createConfig, d.createHost = cfg, host
	if d.imageHealth != nil {
		final := *cfg
		final.Healthcheck = d.imageHealth
		d.createConfig = &final
	}
	return container.CreateResponse{ID: d.newID}, nil
}
func (d *replacingDocker) ContainerStop(_ context.Context, _ string, opts container.StopOptions) error {
	d.stopOptions = opts
	d.steps = append(d.steps, "stop-old")
	if d.stopError != nil {
		return d.stopError
	}
	d.running = false
	return nil
}
func (d *replacingDocker) ContainerRename(_ context.Context, id, name string) error {
	if id == d.oldID {
		d.oldName = name
		d.steps = append(d.steps, "rename-old")
	} else {
		d.steps = append(d.steps, "rename-new")
	}
	return nil
}
func (d *replacingDocker) ContainerStart(_ context.Context, id string, _ container.StartOptions) error {
	if id == d.oldID {
		d.steps = append(d.steps, "start-old")
		d.running = true
	} else {
		d.steps = append(d.steps, "start-new")
		if d.failStart {
			return errors.New("port already in use")
		}
	}
	return nil
}
func (d *replacingDocker) ContainerRemove(_ context.Context, id string, opts container.RemoveOptions) error {
	if opts.RemoveVolumes {
		return errors.New("must keep all mounted volumes")
	}
	if id == d.oldID {
		d.steps = append(d.steps, "remove-old")
	} else {
		d.steps = append(d.steps, "remove-new")
	}
	return nil
}

func TestContainerReplacementRestoresOriginalWhenStartFails(t *testing.T) {
	for _, fail := range []bool{false, true} {
		d := &replacingDocker{oldID: strings.Repeat("a", 64), newID: strings.Repeat("b", 64), oldName: "web", running: true, failStart: fail}
		mode := "new"
		ports := []string{"127.0.0.1:8080:80/tcp"}
		result, err := (Runner{Docker: d}).Run(context.Background(), nodeops.Request{
			Action: nodeops.ContainerReplace, Target: d.oldID, Edit: &nodeops.ContainerEdit{Ports: &ports, Environment: map[string]*string{"MODE": &mode}},
		})
		want := []string{"create", "stop-old", "rename-old", "rename-new", "start-new"}
		if fail {
			want = append(want, "remove-new", "rename-old", "start-old")
			if err == nil || !d.running || d.oldName != "web" {
				t.Fatalf("original not restored: %+v %v", d, err)
			}
		} else {
			want = append(want, "remove-old")
			if err != nil || result.Container == nil || result.Container.FullID != d.newID {
				t.Fatalf("replacement failed: %+v %v", result, err)
			}
		}
		if !slices.Equal(d.steps, want) || !slices.Contains(d.createConfig.Env, "TOKEN=keep-only-on-node") ||
			!slices.Contains(d.createConfig.Env, "MODE=new") || len(d.createHost.Mounts) != 1 || d.createHost.Mounts[0].Source != "app-data" ||
			d.createHost.PortBindings["80/tcp"][0].HostPort != "8080" {
			t.Fatalf("replacement lost configuration: docker=%+v", d)
		}
	}
}

func TestContainerReplacePullsMissingEditedImageBeforeMutating(t *testing.T) {
	for _, pullFails := range []bool{false, true} {
		d := &replacingDocker{oldID: strings.Repeat("a", 64), newID: strings.Repeat("b", 64), oldName: "web", running: true, imageMissing: true}
		const ref = "nginx:alpine"
		r := Runner{Docker: d, PullImage: func(_ context.Context, image string) error {
			if image != ref {
				t.Fatalf("unexpected pull %q", image)
			}
			d.steps = append(d.steps, "pull")
			if pullFails {
				return errors.New("cannot pull")
			}
			return nil
		}}
		_, err := r.Run(t.Context(), nodeops.Request{Action: nodeops.ContainerReplace, Target: d.oldID, Edit: &nodeops.ContainerEdit{Image: ref}})
		if pullFails {
			if err == nil || err.Error() != "Docker could not pull the container image" || !slices.Equal(d.steps, []string{"pull"}) || !d.running {
				t.Fatalf("failed pull touched original: %v %v", d.steps, err)
			}
		} else if err != nil || len(d.steps) < 2 || d.steps[0] != "pull" || d.steps[1] != "create" {
			t.Fatalf("replacement did not pull before creating: %v %v", d.steps, err)
		}
	}
}

func TestMissingContainerHasSpecificErrorAndNoMutation(t *testing.T) {
	d := &replacingDocker{oldID: strings.Repeat("a", 64), oldMissing: true}
	_, err := (Runner{Docker: d}).Run(t.Context(), nodeops.Request{Action: nodeops.ContainerRestart, Target: d.oldID})
	if err == nil || err.Error() != "Docker container no longer exists on this node" || len(d.steps) != 0 {
		t.Fatalf("unexpected result: %v %v", d.steps, err)
	}
}

func TestReplacementReusesStructuredAnonymousVolume(t *testing.T) {
	d := &replacingDocker{oldID: strings.Repeat("a", 64)}
	old, err := d.ContainerInspect(context.Background(), d.oldID)
	if err != nil {
		t.Fatal(err)
	}
	old.HostConfig.Mounts = []mount.Mount{{
		Type: mount.TypeVolume, Target: "/data",
		VolumeOptions: &mount.VolumeOptions{NoCopy: true},
	}}
	_, host, _, err := prepareReplacement(old, nodeops.ContainerEdit{})
	if err != nil {
		t.Fatal(err)
	}
	got := host.Mounts[0]
	if len(host.Mounts) != 1 || got.Target != "/data" || got.Source != "app-data" || got.VolumeOptions == nil || !got.VolumeOptions.NoCopy {
		t.Fatalf("anonymous volume must be reused with its original options: %+v", got)
	}
	if old.HostConfig.Mounts[0].Source != "" {
		t.Fatal("preparing replacement changed the original mount configuration")
	}
}

func TestReplacementVolumeEditsPreserveUnchangedMountOptions(t *testing.T) {
	d := &replacingDocker{oldID: strings.Repeat("a", 64)}
	old, err := d.ContainerInspect(context.Background(), d.oldID)
	if err != nil {
		t.Fatal(err)
	}
	old.Mounts = append(old.Mounts,
		container.MountPoint{Type: mount.TypeVolume, Name: "legacy", Destination: "/legacy", RW: false},
		container.MountPoint{Type: mount.TypeBind, Source: "/srv/app/config", Destination: "/etc/app", RW: false},
	)
	old.HostConfig.Binds = []string{"legacy:/legacy:ro,nocopy", "/srv/app/config:/etc/app:ro"}
	old.HostConfig.Mounts = []mount.Mount{{
		Type: mount.TypeVolume, Target: "/data", Source: "app-data",
		VolumeOptions: &mount.VolumeOptions{Subpath: "state", NoCopy: true},
	}}
	volumes := []string{"app-data:/data:ro", "legacy:/legacy:ro", "new:/extra"}
	_, host, _, err := prepareReplacement(old, nodeops.ContainerEdit{Volumes: &volumes})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(host.Binds, old.HostConfig.Binds) || len(host.Mounts) != 2 {
		t.Fatalf("changed mount representation: %+v, %+v", host.Binds, host.Mounts)
	}
	got := host.Mounts[0]
	if got.Target != "/data" || !got.ReadOnly || got.VolumeOptions == nil ||
		got.VolumeOptions.Subpath != "state" || !got.VolumeOptions.NoCopy || host.Mounts[1].Source != "new" {
		t.Fatalf("volume edit lost old options: %+v", host.Mounts)
	}
}

func TestReplacementUsesOriginalStopPolicyAndHealthWindow(t *testing.T) {
	grace := 30
	d := &replacingDocker{
		oldID: strings.Repeat("a", 64), newID: strings.Repeat("b", 64),
		oldName: "web", running: true, stopTimeout: &grace,
		health: &container.HealthConfig{Test: []string{"CMD", "check"}, Interval: 90 * time.Second, Timeout: time.Second},
	}
	_, err := (Runner{Docker: d}).Run(context.Background(), nodeops.Request{
		Action: nodeops.ContainerReplace, Target: d.oldID, Edit: &nodeops.ContainerEdit{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.stopOptions.Timeout != nil || d.createConfig.StopTimeout == nil || *d.createConfig.StopTimeout != grace {
		t.Fatalf("unexpected override of the configured stop policy: %+v", d.stopOptions)
	}
	if d.healthChecks < 2 || d.healthWait <= time.Minute || d.healthWait >= nodeops.ContainerReplace.Timeout()-replacementRecoveryTimeout-replacementCleanupMargin {
		t.Fatalf("replacement ignored a slow but supported health check: checked %d times with %s budget", d.healthChecks, d.healthWait)
	}
}

func TestReplacementRejectsInsufficientBudgetBeforeStopping(t *testing.T) {
	for _, tc := range []struct {
		name   string
		grace  int
		health *container.HealthConfig
	}{
		{name: "infinite stop", grace: -1},
		{name: "long stop", grace: 200},
		{name: "long health check", grace: 10, health: &container.HealthConfig{Test: []string{"CMD", "check"}, Interval: 5 * time.Minute}},
		{name: "combined budget", grace: 80, health: &container.HealthConfig{Test: []string{"CMD", "check"}, Interval: 80 * time.Second}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &replacingDocker{
				oldID: strings.Repeat("a", 64), newID: strings.Repeat("b", 64),
				oldName: "web", running: true, stopTimeout: &tc.grace, health: tc.health,
			}
			_, err := (Runner{Docker: d}).Run(context.Background(), nodeops.Request{
				Action: nodeops.ContainerReplace, Target: d.oldID, Edit: &nodeops.ContainerEdit{},
			})
			if err == nil || !d.running || len(d.steps) != 0 {
				t.Fatalf("invalid replacement budget must fail before creating or stopping anything: %v, %v", err, d.steps)
			}
		})
	}
}

func TestReplacementChecksNewImageDefaultsBeforeStopping(t *testing.T) {
	d := &replacingDocker{
		oldID: strings.Repeat("a", 64), newID: strings.Repeat("b", 64), oldName: "web", running: true,
		imageHealth: &container.HealthConfig{Test: []string{"CMD", "check"}, Interval: 5 * time.Minute},
	}
	_, err := (Runner{Docker: d}).Run(context.Background(), nodeops.Request{
		Action: nodeops.ContainerReplace, Target: d.oldID, Edit: &nodeops.ContainerEdit{Image: "web:new"},
	})
	if err == nil || !d.running || !slices.Equal(d.steps, []string{"create", "remove-new"}) {
		t.Fatalf("inherited health check must leave the original running: %v, %v", err, d.steps)
	}
}

func TestReplacementStopWithoutAcknowledgementHasUnknownOutcome(t *testing.T) {
	d := &replacingDocker{
		oldID: strings.Repeat("a", 64), newID: strings.Repeat("b", 64), oldName: "web", running: true,
		stopError: context.DeadlineExceeded,
	}
	_, err := (Runner{Docker: d}).Run(context.Background(), nodeops.Request{
		Action: nodeops.ContainerReplace, Target: d.oldID, Edit: &nodeops.ContainerEdit{},
	})
	if !errors.Is(err, nodeops.ErrOutcomeUnknown) || d.oldName != "web" ||
		!slices.Equal(d.steps, []string{"create", "stop-old", "remove-new"}) {
		t.Fatalf("unconfirmed stop must not be reported as a successful recovery: %v, %v", err, d.steps)
	}
}
