package nodeops

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"

	"github.com/maxtanforge/moori/internal/core/nodeops"
)

type guardedDocker struct {
	Docker
	containerID string
	networkID   string
	called      string
	listAll     bool
	standalone  bool
	state       *container.State
	removeOpts  container.RemoveOptions
}

func (d *guardedDocker) ContainerInspect(_ context.Context, id string) (container.InspectResponse, error) {
	labels := map[string]string{"com.docker.compose.project": "managed"}
	if d.standalone {
		labels = nil
	}
	return container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{ID: id, State: d.state}, Config: &container.Config{Labels: labels}}, nil
}

func (d *guardedDocker) ContainerRestart(context.Context, string, container.StopOptions) error {
	d.called = "container.restart"
	return io.ErrUnexpectedEOF
}

func (d *guardedDocker) ContainerStart(context.Context, string, container.StartOptions) error {
	d.called = "container.start"
	d.state = &container.State{Status: "restarting", Restarting: true}
	return nil
}

func (d *guardedDocker) ContainerStop(context.Context, string, container.StopOptions) error {
	d.called = "container.stop"
	d.state = &container.State{Status: "exited"}
	return nil
}

func TestContainerLifecycleReturnsObservedState(t *testing.T) {
	for _, tc := range []struct {
		action nodeops.Action
		before container.ContainerState
		after  string
	}{
		{nodeops.ContainerStart, "exited", "restarting"},
		{nodeops.ContainerStop, "running", "exited"},
	} {
		t.Run(string(tc.action), func(t *testing.T) {
			daemon := &guardedDocker{
				containerID: strings.Repeat("a", 64),
				state:       &container.State{Status: tc.before},
			}
			result, err := (Runner{Docker: daemon}).Run(context.Background(), nodeops.Request{Action: tc.action, Target: daemon.containerID})
			if err != nil || daemon.called != string(tc.action) || result.Container == nil ||
				result.Container.FullID != daemon.containerID || result.Container.State != tc.after {
				t.Fatalf("Docker call=%q container=%+v err=%v", daemon.called, result.Container, err)
			}
			if _, err := time.Parse(time.RFC3339Nano, result.Container.StateObservedAt); err != nil {
				t.Fatalf("missing node observation timestamp: %v", err)
			}
		})
	}
}

func (d *guardedDocker) NetworkInspect(_ context.Context, id string, _ network.InspectOptions) (network.Inspect, error) {
	return network.Inspect{ID: id, Name: "custom", Scope: "local", Driver: "bridge"}, nil
}

func (d *guardedDocker) NetworkConnect(context.Context, string, string, *network.EndpointSettings) error {
	d.called = "network.connect"
	return errors.New("unsafe connect")
}

func (d *guardedDocker) VolumeInspect(_ context.Context, name string) (volume.Volume, error) {
	return volume.Volume{Name: name, Driver: "local", Scope: "local"}, nil
}

func (d *guardedDocker) ContainerList(_ context.Context, opts container.ListOptions) ([]container.Summary, error) {
	d.listAll = opts.All
	return []container.Summary{{ID: d.containerID, Mounts: []container.MountPoint{{Name: "persistent_data"}}}}, nil
}

func (d *guardedDocker) VolumeRemove(context.Context, string, bool) error {
	d.called = "volume.remove"
	return errors.New("unsafe removal")
}

func TestContainerRestartTransportFailureHasUnknownOutcome(t *testing.T) {
	daemon := &guardedDocker{containerID: strings.Repeat("a", 64)}
	_, err := (Runner{Docker: daemon}).Run(context.Background(), nodeops.Request{Action: nodeops.ContainerRestart, Target: daemon.containerID})
	if !errors.Is(err, nodeops.ErrOutcomeUnknown) || daemon.called != "container.restart" {
		t.Fatalf("err=%v docker mutation=%q", err, daemon.called)
	}
}

func TestComposeNetworkChangeDoesNotReachDocker(t *testing.T) {
	daemon := &guardedDocker{containerID: strings.Repeat("a", 64), networkID: strings.Repeat("b", 64)}
	_, err := (Runner{Docker: daemon}).Run(context.Background(), nodeops.Request{Action: nodeops.NetworkConnect, Target: daemon.networkID, Container: daemon.containerID})
	if err == nil || daemon.called != "" {
		t.Fatalf("err=%v docker mutation=%q", err, daemon.called)
	}
}

func TestRemovingVolumeChecksStoppedContainerReferences(t *testing.T) {
	daemon := &guardedDocker{containerID: strings.Repeat("a", 64)}
	_, err := (Runner{Docker: daemon}).Run(context.Background(), nodeops.Request{Action: nodeops.VolumeDelete, Target: "persistent_data"})
	if err == nil || daemon.called != "" || !daemon.listAll {
		t.Fatalf("err=%v docker mutation=%q include stopped=%v", err, daemon.called, daemon.listAll)
	}
}

func (d *guardedDocker) ContainerPause(context.Context, string) error {
	d.called = "container.pause"
	return nil
}

func (d *guardedDocker) ContainerUnpause(context.Context, string) error {
	d.called = "container.resume"
	return nil
}

func (d *guardedDocker) ContainerRemove(_ context.Context, _ string, opts container.RemoveOptions) error {
	d.called = "container.remove"
	d.removeOpts = opts
	return nil
}

func TestContainerMaintenance(t *testing.T) {
	for _, tc := range []struct {
		action     nodeops.Action
		state      *container.State
		standalone bool
		wantCall   bool
	}{
		{"container.pause", &container.State{Status: "running", Running: true}, true, true},
		{"container.resume", &container.State{Status: "paused", Running: true, Paused: true}, true, true},
		{"container.remove", &container.State{Status: "exited"}, true, true},
		{"container.remove", &container.State{Status: "exited"}, false, false},
		{"container.remove", &container.State{Status: "running", Running: true}, true, false},
	} {
		daemon := &guardedDocker{containerID: strings.Repeat("a", 64), standalone: tc.standalone, state: tc.state}
		_, err := (Runner{Docker: daemon}).Run(context.Background(), nodeops.Request{Action: tc.action, Target: daemon.containerID})
		if tc.wantCall && (err != nil || daemon.called != string(tc.action)) || !tc.wantCall && (err == nil || daemon.called != "") {
			t.Errorf("%s, status %s, standalone %v: call %q, err %v", tc.action, tc.state.Status, tc.standalone, daemon.called, err)
		}
		if daemon.removeOpts.Force || daemon.removeOpts.RemoveVolumes || daemon.removeOpts.RemoveLinks {
			t.Errorf("unsafe removal options: %+v", daemon.removeOpts)
		}
	}
}
