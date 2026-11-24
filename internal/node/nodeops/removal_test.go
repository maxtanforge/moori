package nodeops

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/errdefs"

	"github.com/maxtanforge/moori/internal/core/nodeops"
)

type removalDocker struct {
	Docker
	compose                   bool
	removed, stopped, volumes []string
	volumeErr                 error
	extraMounts               []container.MountPoint
}

func (d *removalDocker) ContainerInspect(_ context.Context, id string) (container.InspectResponse, error) {
	labels := map[string]string{}
	state := &container.State{Status: "exited"}
	if d.compose {
		labels[composeProjectLabel] = "app"
		state.Running = true
		state.Status = "running"
	}
	c := container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{ID: id, State: state}, Config: &container.Config{Labels: labels}, Mounts: []container.MountPoint{
		{Type: "volume", Name: "app_data"}, {Type: "volume", Name: "app_data"}, {Type: "bind", Source: "/host/data"},
	}}
	if d.compose {
		c.Mounts = append(c.Mounts, container.MountPoint{Type: "volume", Name: "external_data"})
	}
	c.Mounts = append(c.Mounts, d.extraMounts...)
	return c, nil
}
func (d *removalDocker) ContainerList(_ context.Context, o container.ListOptions) ([]container.Summary, error) {
	if !o.All || !o.Filters.MatchKVList("label", map[string]string{composeProjectLabel: "app"}) {
		panic("missing project filter")
	}
	return []container.Summary{{ID: strings.Repeat("a", 64), Labels: map[string]string{composeProjectLabel: "app"}}, {ID: strings.Repeat("b", 64), Labels: map[string]string{composeProjectLabel: "other"}}}, nil
}
func (d *removalDocker) ContainerStop(_ context.Context, id string, _ container.StopOptions) error {
	d.stopped = append(d.stopped, id)
	return nil
}
func (d *removalDocker) ContainerRemove(_ context.Context, id string, o container.RemoveOptions) error {
	if o.Force || o.RemoveVolumes {
		panic("unsafe implicit removal")
	}
	d.removed = append(d.removed, id)
	return nil
}
func (d *removalDocker) VolumeInspect(_ context.Context, name string) (volume.Volume, error) {
	v := volume.Volume{Name: name, Scope: "local"}
	if name == "cluster_data" {
		v.Scope = "global"
	}
	if (d.compose && name != "external_data") || name == "compose_owned" {
		v.Labels = map[string]string{composeProjectLabel: "app"}
	}
	return v, nil
}
func (d *removalDocker) VolumeRemove(_ context.Context, name string, force bool) error {
	if force {
		panic("forced volume removal")
	}
	d.volumes = append(d.volumes, name)
	if name == "app_data" {
		return d.volumeErr
	}
	return nil
}
func TestRemovalPreservesNetworksAndKeepsVolumesUnlessSelected(t *testing.T) {
	for _, compose := range []bool{false, true} {
		for _, removeVolumes := range []bool{false, true} {
			d := &removalDocker{compose: compose}
			req := nodeops.Request{Action: nodeops.ContainerRemove, Target: strings.Repeat("a", 64), RemoveVolumes: removeVolumes}
			if compose {
				req.Action = nodeops.ComposeRemove
				req.Target = "app"
			}
			result, err := (Runner{Docker: d}).Run(context.Background(), req)
			if err != nil || len(d.removed) != 1 || (len(d.volumes) == 1) != removeVolumes {
				t.Fatalf("compose=%v selected=%v removed=%v volumes=%v err=%v", compose, removeVolumes, d.removed, d.volumes, err)
			}
			if compose {
				if _, err := time.Parse(time.RFC3339Nano, result.ResourceObservedAt); err != nil {
					t.Fatalf("missing compose removal observation time: %v", err)
				}
			}
			if compose && len(d.stopped) != 1 {
				t.Fatal(d)
			}
		}
	}
}

func TestSharedVolumeFailureReportsContainerAlreadyRemoved(t *testing.T) {
	d := &removalDocker{volumeErr: errdefs.Conflict(errors.New("in use"))}
	_, err := (Runner{Docker: d}).Run(context.Background(), nodeops.Request{Action: nodeops.ContainerRemove, Target: strings.Repeat("a", 64), RemoveVolumes: true})
	if err == nil || !strings.Contains(err.Error(), "容器已删除") || len(d.removed) != 1 {
		t.Fatal(err, d.removed)
	}
}

func TestComposeCleanupContinuesAfterAReferencedVolume(t *testing.T) {
	d := &removalDocker{compose: true, volumeErr: errdefs.Conflict(errors.New("in use")), extraMounts: []container.MountPoint{
		{Type: "volume", Name: "another_local_volume"}, {Type: "volume", Name: "cluster_data"},
	}}
	_, err := (Runner{Docker: d}).Run(context.Background(), nodeops.Request{Action: nodeops.ComposeRemove, Target: "app", RemoveVolumes: true})
	if err == nil || !strings.Contains(err.Error(), "app_data") || strings.Join(d.volumes, ",") != "app_data,another_local_volume" {
		t.Fatalf("cleanup error=%v volumes=%v", err, d.volumes)
	}
}

func TestContainerCleanupPreservesComposeAndNonLocalVolumes(t *testing.T) {
	d := &removalDocker{extraMounts: []container.MountPoint{{Type: "volume", Name: "compose_owned"}, {Type: "volume", Name: "cluster_data"}}}
	_, err := (Runner{Docker: d}).Run(context.Background(), nodeops.Request{Action: nodeops.ContainerRemove, Target: strings.Repeat("a", 64), RemoveVolumes: true})
	if err != nil || strings.Join(d.volumes, ",") != "app_data" {
		t.Fatalf("cleanup error=%v volumes=%v", err, d.volumes)
	}
}

func TestRemovalOptionsAreScoped(t *testing.T) {
	for _, req := range []nodeops.Request{
		{Action: nodeops.ContainerStart, Target: strings.Repeat("a", 64), RemoveVolumes: true},
		{Action: nodeops.ComposeRemove, Target: "moori-" + strings.Repeat("a", 32)},
		{Action: nodeops.ComposeRemove, Target: "app", ForceRecreate: true},
	} {
		if req.Validate() == nil {
			t.Fatal("accepted invalid request", req)
		}
	}
}
