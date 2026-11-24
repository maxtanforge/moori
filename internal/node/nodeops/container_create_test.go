package nodeops

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/maxtanforge/moori/internal/core/nodeops"
)

type createDocker struct {
	Docker
	imageMissing, volumeMissing    bool
	createErr, startErr, removeErr error
	newID, started, removed        string
	name                           string
	config                         *container.Config
	host                           *container.HostConfig
	endpoints                      *network.NetworkingConfig
	removeOptions                  container.RemoveOptions
}

func (d *createDocker) ImageInspect(context.Context, string, ...client.ImageInspectOption) (image.InspectResponse, error) {
	if d.imageMissing {
		return image.InspectResponse{}, errdefs.NotFound(errors.New("image unavailable"))
	}
	return image.InspectResponse{ID: "sha256:" + strings.Repeat("c", 64)}, nil
}

func (d *createDocker) VolumeInspect(_ context.Context, name string) (volume.Volume, error) {
	if d.volumeMissing {
		return volume.Volume{}, errdefs.NotFound(errors.New("volume unavailable"))
	}
	return volume.Volume{Name: name, Driver: "local", Scope: "local"}, nil
}

func (d *createDocker) NetworkInspect(_ context.Context, name string, _ network.InspectOptions) (network.Inspect, error) {
	return network.Inspect{ID: strings.Repeat("b", 64), Name: name, Driver: "bridge", Scope: "local"}, nil
}

func (d *createDocker) ContainerCreate(_ context.Context, config *container.Config, host *container.HostConfig, endpoints *network.NetworkingConfig, _ *v1.Platform, name string) (container.CreateResponse, error) {
	d.name, d.config, d.host, d.endpoints = name, config, host, endpoints
	return container.CreateResponse{ID: d.newID}, d.createErr
}

func (d *createDocker) ContainerInspect(_ context.Context, id string) (container.InspectResponse, error) {
	if id != d.started || d.startErr != nil {
		return container.InspectResponse{}, errdefs.NotFound(errors.New("container unavailable"))
	}
	return container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{ID: id, Name: "/" + d.name, State: &container.State{Status: "running", Running: true}},
		Config:            d.config,
	}, nil
}

func (d *createDocker) ContainerStart(_ context.Context, id string, _ container.StartOptions) error {
	d.started = id
	return d.startErr
}

func (d *createDocker) ContainerRemove(_ context.Context, id string, opts container.RemoveOptions) error {
	d.removed, d.removeOptions = id, opts
	return d.removeErr
}

func TestCreateStandaloneContainer(t *testing.T) {
	id := strings.Repeat("a", 64)
	d := &createDocker{newID: id}
	r := Runner{Docker: d}
	volumes := []string{"persistent:/var/lib/app:ro"}
	networks := []string{"frontend"}
	port := []string{"127.0.0.1:8080:80/tcp"}
	mode := "on-failure:3"
	value := "enabled"
	req := nodeops.Request{Action: "container.create", Name: "app", Edit: &nodeops.ContainerEdit{
		Image: "nginx:alpine", Restart: &mode, Ports: &port, Volumes: &volumes, Networks: &networks,
		Environment: map[string]*string{"FEATURE": &value},
		Labels:      map[string]*string{"app.feature": &value},
	}}
	got, err := r.Run(context.Background(), req)
	if err != nil || got.Container == nil || got.Container.FullID != id || got.Container.Name != req.Name || got.Container.State != "running" || got.ResourceObservedAt == "" {
		t.Fatalf("creation result = %+v (%v)", got, err)
	}
	if d.started != id || d.name != req.Name || d.config.Image != req.Edit.Image ||
		d.host.RestartPolicy.Name != "on-failure" || d.host.RestartPolicy.MaximumRetryCount != 3 ||
		len(d.host.PortBindings["80/tcp"]) != 1 || len(d.host.Mounts) != 1 ||
		d.host.Mounts[0].Type != mount.TypeVolume || d.host.Mounts[0].Source != "persistent" || !d.host.Mounts[0].ReadOnly ||
		d.config.Env[0] != "FEATURE=enabled" || d.config.Labels["app.feature"] != value ||
		d.endpoints.EndpointsConfig["frontend"] == nil {
		t.Fatalf("Docker create/start did not use the validated selections: %+v", d)
	}
}

func TestCreateDoesNotRemoveExistingContainerOrVolumes(t *testing.T) {
	id := strings.Repeat("a", 64)
	for _, tc := range []struct {
		name             string
		setup            func(*createDocker)
		cleanup, unknown bool
	}{
		{"name conflict", func(d *createDocker) { d.createErr = errdefs.Conflict(errors.New("same name")) }, false, false},
		{"create transport failure", func(d *createDocker) { d.createErr = io.ErrUnexpectedEOF }, false, true},
		{"definite start failure", func(d *createDocker) { d.startErr = errdefs.InvalidParameter(errors.New("start rejected")) }, true, false},
		{"uncertain start failure", func(d *createDocker) { d.startErr = io.ErrUnexpectedEOF }, false, true},
		{"cleanup fails", func(d *createDocker) {
			d.startErr = errdefs.InvalidParameter(errors.New("start rejected"))
			d.removeErr = io.ErrUnexpectedEOF
		}, true, true},
		{"missing preselected volume", func(d *createDocker) { d.volumeMissing = true }, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &createDocker{newID: id}
			tc.setup(d)
			volumes := []string{"persistent:/data"}
			req := nodeops.Request{Action: "container.create", Name: "new-app", Edit: &nodeops.ContainerEdit{Image: "nginx:alpine", Volumes: &volumes}}
			_, err := (Runner{Docker: d}).Run(context.Background(), req)
			if err == nil || errors.Is(err, nodeops.ErrOutcomeUnknown) != tc.unknown || (d.removed == id) != tc.cleanup || d.removeOptions.Force || d.removeOptions.RemoveVolumes || d.volumeMissing && d.config != nil {
				t.Fatalf("outcome=%v, started=%q removed=%q options=%+v", err, d.started, d.removed, d.removeOptions)
			}
		})
	}
}

func TestCreatePullsOnlyMissingNamedImages(t *testing.T) {
	d := &createDocker{newID: strings.Repeat("a", 64), imageMissing: true}
	var pulls []string
	r := Runner{Docker: d, PullImage: func(_ context.Context, image string) error {
		pulls = append(pulls, image)
		d.imageMissing = false
		return nil
	}}
	req := nodeops.Request{Action: nodeops.ContainerCreate, Name: "new-app", Edit: &nodeops.ContainerEdit{Image: "nginx:alpine"}}
	if _, err := r.Run(context.Background(), req); err != nil || len(pulls) != 1 || pulls[0] != req.Edit.Image {
		t.Fatalf("missing named image result = %v, pulls = %v", err, pulls)
	}
	req.Edit.PullScope = &nodeops.RegistryScope{RegistryID: "shared"}
	if _, err := r.Run(context.Background(), req); err != nil || len(pulls) != 1 {
		t.Fatalf("local image result = %v, pulls = %v", err, pulls)
	}
	req.Edit.PullScope = nil
	req.Edit.Image = "sha256:" + strings.Repeat("c", 64)
	d.imageMissing = true
	if _, err := r.Run(context.Background(), req); err == nil || len(pulls) != 1 {
		t.Fatalf("missing local ID result = %v, pulls = %v", err, pulls)
	}
}

func TestBindEditsPreserveUnchangedOptionsAndNamedVolumes(t *testing.T) {
	host := &container.HostConfig{
		Binds: []string{"data:/data", "/srv/legacy:/legacy:ro,z"},
		Mounts: []mount.Mount{
			{Type: mount.TypeBind, Source: "/srv/structured", Target: "/stored", BindOptions: &mount.BindOptions{Propagation: mount.PropagationRShared}},
			{Type: mount.TypeVolume, Source: "cache", Target: "/cache"},
		},
	}
	edits := []string{"/srv/legacy:/legacy:rw", "/srv/structured:/stored:ro", "/srv/new:/new:ro"}
	prepareBindMounts(host, &edits)
	if len(host.Binds) != 2 || host.Binds[0] != "data:/data" || host.Binds[1] != "/srv/legacy:/legacy:z" ||
		len(host.Mounts) != 3 || host.Mounts[0].BindOptions.Propagation != mount.PropagationRShared || !host.Mounts[0].ReadOnly ||
		host.Mounts[1].Type != mount.TypeVolume || host.Mounts[2].Type != mount.TypeBind ||
		host.Mounts[2].Source != "/srv/new" || host.Mounts[2].Target != "/new" || !host.Mounts[2].ReadOnly ||
		host.Mounts[2].BindOptions != nil || duplicateMountTargets(host) {
		t.Fatalf("unexpected bind edit configuration: %+v %+v", host.Binds, host.Mounts)
	}
	edits = []string{"/srv/wrong:/data"}
	prepareBindMounts(host, &edits)
	if !duplicateMountTargets(host) {
		t.Fatal("bind edit overlapped an unchanged named volume")
	}
}

type editConfigDocker struct {
	Docker
	config *container.Config
}

func (d editConfigDocker) ContainerInspect(_ context.Context, id string) (container.InspectResponse, error) {
	return container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{ID: id}, Config: d.config}, nil
}

func TestStandaloneContainerEditConfigurationSeparatesSensitiveValues(t *testing.T) {
	d := editConfigDocker{config: &container.Config{
		Env:    []string{"EQUALS=value=with=equals", "EMPTY=", "UNSET", "MULTILINE=line one\nline two"},
		Labels: map[string]string{"app.annotation": "line one\nline two", "com.docker.example": ""},
	}}
	r := Runner{Docker: d}
	req := nodeops.Request{Action: nodeops.ContainerInspect, Target: strings.Repeat("a", 64)}
	if result, err := r.Run(context.Background(), req); err != nil || result.EditConfig != nil {
		t.Fatalf("normal inspect included configuration or failed: %v", err)
	}
	req.Action = nodeops.ContainerEditConfig
	result, err := r.Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Container != nil || result.EditConfig == nil {
		t.Fatal("configuration was not separated from normal inspect")
	}
	values := result.EditConfig.Environment
	if values["EQUALS"] == nil || *values["EQUALS"] != "value=with=equals" || values["EMPTY"] == nil || *values["EMPTY"] != "" ||
		values["MULTILINE"] == nil || *values["MULTILINE"] != "line one\nline two" {
		t.Fatal("environment values were not preserved")
	}
	if value, exists := values["UNSET"]; !exists || value != nil {
		t.Fatal("an unset value was not retained")
	}
	if result.EditConfig.Labels["app.annotation"] != "line one\nline two" || len(result.EditConfig.Labels) != 2 {
		t.Fatal("label values were not preserved")
	}
	d.config.Labels["com.docker.compose.project"] = "stack"
	if result, err := r.Run(context.Background(), req); err != nil || result.EditConfig == nil ||
		result.EditConfig.Labels["com.docker.compose.project"] != "stack" {
		t.Fatal("Compose container configuration is unavailable for read-only details", err)
	}
}

func TestCreateContainerAppliesResourceLimits(t *testing.T) {
	nano, memory, pids := int64(750000000), int64(128<<20), int64(64)
	d := &createDocker{newID: strings.Repeat("a", 64)}
	req := nodeops.Request{Action: nodeops.ContainerCreate, Name: "test-app", Edit: &nodeops.ContainerEdit{
		Image: "nginx:latest", Resources: &nodeops.ContainerResources{NanoCPUs: &nano, MemoryBytes: &memory, PIDs: &pids},
	}}
	_, err := (Runner{Docker: d}).Run(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	resources := d.host.Resources
	if resources.NanoCPUs != nano || resources.Memory != memory || resources.PidsLimit == nil || *resources.PidsLimit != pids {
		t.Fatalf("Docker resources: %+v", resources)
	}
}
