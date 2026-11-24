package nodeops

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"

	"github.com/maxtanforge/moori/internal/core/nodeops"
)

type releaseDocker struct {
	*replacingDocker
	source string
	labels map[string]string
}

func (d releaseDocker) ContainerInspect(ctx context.Context, key string) (container.InspectResponse, error) {
	if key == "web" {
		key = d.oldID
	}
	out, err := d.replacingDocker.ContainerInspect(ctx, key)
	if err == nil && key == d.oldID {
		out.Config.Image = d.source
		out.Config.Labels = d.labels
	}
	return out, err
}

func (d releaseDocker) ImageInspect(context.Context, string, ...client.ImageInspectOption) (image.InspectResponse, error) {
	return image.InspectResponse{}, errdefs.NotFound(errors.New("not pulled"))
}

func TestContainerReleaseUsesReplacementOnlyForMatchingStandaloneRepository(t *testing.T) {
	const repo = "registry.example/team/web"
	ref := repo + "@sha256:" + strings.Repeat("d", 64)
	for _, tc := range []struct {
		name, source                   string
		compose, pullFails, startFails bool
	}{
		{name: "success", source: repo + ":v1"},
		{name: "same digest", source: ref},
		{name: "wrong repository", source: "registry.example/team/other:v1"},
		{name: "compose", source: repo + ":v1", compose: true},
		{name: "failed pull", source: repo + ":v1", pullFails: true},
		{name: "failed start restores original", source: repo + ":v1", startFails: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := releaseDocker{replacingDocker: &replacingDocker{oldID: strings.Repeat("a", 64), newID: strings.Repeat("b", 64),
				oldName: "web", running: true, failStart: tc.startFails}, source: tc.source}
			if tc.compose {
				d.labels = map[string]string{"com.docker.compose.project": "app"}
			}
			r := Runner{Docker: d, PullImage: func(_ context.Context, image string) error {
				d.steps = append(d.steps, "pull")
				if image != ref || tc.pullFails {
					return errors.New("pull failed")
				}
				return nil
			}}
			result, err := r.Run(t.Context(), nodeops.Request{Action: nodeops.ContainerRelease, Name: "web", Image: ref})
			switch tc.name {
			case "success":
				if err != nil || result.Container.FullID != d.newID || d.createConfig.Image != ref ||
					!slices.Contains(d.createConfig.Env, "MODE=old") || len(d.createHost.Mounts) != 1 ||
					!slices.Equal(d.steps, []string{"pull", "create", "stop-old", "rename-old", "rename-new", "start-new", "remove-old"}) {
					t.Fatalf("replacement failed or lost original configuration, steps %v, error %v", d.steps, err)
				}
			case "same digest":
				if err != nil || result.Container.FullID != d.oldID || len(d.steps) != 0 {
					t.Fatalf("same digest was reapplied, steps %v, error %v", d.steps, err)
				}
			case "failed start restores original":
				if err == nil || !d.running || d.oldName != "web" || !slices.Contains(d.steps, "start-old") || slices.Contains(d.steps, "remove-old") {
					t.Fatalf("original was not restored, steps %v, error %v", d.steps, err)
				}
			case "failed pull":
				if err == nil || !slices.Equal(d.steps, []string{"pull"}) {
					t.Fatalf("failed pull touched the container, steps %v, error %v", d.steps, err)
				}
			default:
				if err == nil || len(d.steps) != 0 {
					t.Fatalf("disallowed target was touched, steps %v, error %v", d.steps, err)
				}
			}
		})
	}
}
