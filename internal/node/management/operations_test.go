package management

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"

	"github.com/maxtanforge/moori/internal/core/nodeops"
	"github.com/maxtanforge/moori/internal/core/registry"
	"github.com/maxtanforge/moori/internal/node/composefiles"
	dockerops "github.com/maxtanforge/moori/internal/node/nodeops"
	noderegistry "github.com/maxtanforge/moori/internal/node/registry"
)

type missingImageDocker struct{ dockerops.Docker }

func (missingImageDocker) ImageInspect(context.Context, string, ...client.ImageInspectOption) (image.InspectResponse, error) {
	return image.InspectResponse{}, errdefs.NotFound(errors.New("missing"))
}

func TestCreateResolvesSharedCredentialsWithOrWithoutRegistryID(t *testing.T) {
	req := nodeops.Request{Action: nodeops.ContainerCreate, Name: "new-app", Operation: "job-1",
		Edit: &nodeops.ContainerEdit{Image: "registry.example/team/app:v2"}}
	var anonymous, fetches int
	ops := Operations{
		docker: &dockerops.Runner{Docker: missingImageDocker{}, PullImage: func(context.Context, string) error {
			anonymous++
			return errors.New("private transport detail")
		}},
		images: &noderegistry.Engine{},
		credentials: func(_ context.Context, id string) (registry.Credential, error) {
			if id != req.Operation {
				t.Fatalf("credential fetch used %q", id)
			}
			fetches++
			return registry.Credential{}, errors.New("private credential provider detail")
		},
	}
	_, err := ops.Run(t.Context(), req)
	if err != registry.ErrOperationCredentials || anonymous != 0 || fetches != 1 {
		t.Fatalf("anonymous=%d fetches=%d err=%v", anonymous, fetches, err)
	}
	req.Edit.PullScope = &nodeops.RegistryScope{RegistryID: "private", Project: "team"}
	_, err = ops.Run(t.Context(), req)
	if err != registry.ErrOperationCredentials || anonymous != 0 || fetches != 2 {
		t.Fatalf("anonymous=%d fetches=%d err=%v", anonymous, fetches, err)
	}
}

func TestComposeVolumeCleanupRequiresAuthorizedSource(t *testing.T) {
	t.Setenv("MOORI_COMPOSE_SOURCE_ROOT", "")
	t.Setenv("MOORI_COMPOSE_SOURCES", "")
	_, err := (&Operations{}).Run(context.Background(), nodeops.Request{
		Action:        nodeops.ComposeRemove,
		Target:        "external-app",
		RemoveVolumes: true,
	})
	if !errors.Is(err, composefiles.ErrSourceUnavailable) {
		t.Fatalf("remove without a source: %v", err)
	}
}

type externalComposeDocker struct {
	dockerops.Docker
	id      string
	removed string
}

func (d *externalComposeDocker) ContainerList(context.Context, container.ListOptions) ([]container.Summary, error) {
	return []container.Summary{{ID: d.id, Labels: map[string]string{"com.docker.compose.project": "external-app"}}}, nil
}

func (d *externalComposeDocker) ContainerInspect(context.Context, string) (container.InspectResponse, error) {
	return container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{ID: d.id, State: &container.State{Status: "exited"}},
		Config:            &container.Config{Labels: map[string]string{"com.docker.compose.project": "external-app"}},
	}, nil
}

func (d *externalComposeDocker) ContainerRemove(_ context.Context, id string, options container.RemoveOptions) error {
	if options.RemoveVolumes || options.Force {
		return errors.New("unexpected removal options")
	}
	d.removed = id
	return nil
}

func TestComposeRemoveWithoutSourceReachesDocker(t *testing.T) {
	t.Setenv("MOORI_COMPOSE_SOURCE_ROOT", "")
	t.Setenv("MOORI_COMPOSE_SOURCES", "")
	d := &externalComposeDocker{id: strings.Repeat("a", 64)}
	ops := Operations{docker: &dockerops.Runner{Docker: d}}
	_, err := ops.Run(context.Background(), nodeops.Request{Action: nodeops.ComposeRemove, Target: "external-app"})
	if err != nil || d.removed != d.id {
		t.Fatalf("removed %q, err %v", d.removed, err)
	}
}
