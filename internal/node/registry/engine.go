package registry

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/maxtanforge/moori/internal/core/registry"
	"github.com/docker/docker/api/types/image"
	dockerRegistry "github.com/docker/docker/api/types/registry"
	"github.com/docker/docker/client"
)

var (
	errPull   = errors.New("Docker could not pull the registry image")
	errVerify = errors.New("Docker could not verify the registry image")
)

type Engine struct{ client *client.Client }

func NewLocalEngine() (*Engine, error) {
	cli, err := client.NewClientWithOpts(client.WithHost("unix:///var/run/docker.sock"), client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return &Engine{client: cli}, nil
}

func (e *Engine) Close() error { return e.client.Close() }

func engineRegistryAuth(ref registry.Image, credential registry.Credential) (string, error) {
	if credential == (registry.Credential{}) {
		return "", nil
	}
	if credential.ServerAddress != ref.Host || credential.Username == "" || credential.Password == "" {
		return "", registry.ErrDenied
	}
	server := credential.ServerAddress
	if server == "docker.io" {
		server = "https://index.docker.io/v1/"
	}
	return dockerRegistry.EncodeAuthConfig(dockerRegistry.AuthConfig{Username: credential.Username, Password: credential.Password, ServerAddress: server})
}

func (e *Engine) Pull(ctx context.Context, value, platform string, credential registry.Credential) error {
	ref, err := registry.ParseImage(value)
	if err != nil {
		return errPull
	}
	auth, err := engineRegistryAuth(ref, credential)
	if err != nil {
		return errPull
	}
	body, err := e.client.ImagePull(ctx, ref.Reference, image.PullOptions{RegistryAuth: auth, Platform: platform})
	if err != nil {
		return errPull
	}
	defer body.Close()
	progress := &io.LimitedReader{R: body, N: 32 << 20}
	decoder := json.NewDecoder(progress)
	for {
		var event struct {
			Error       string          `json:"error"`
			ErrorDetail json.RawMessage `json:"errorDetail"`
		}
		err := decoder.Decode(&event)
		if err == io.EOF && progress.N > 0 {
			break
		}
		if err != nil || event.Error != "" || len(event.ErrorDetail) > 0 && string(event.ErrorDetail) != "null" {
			return errPull
		}
	}
	// The workload can only start after Docker has persisted the exact reference.
	inspected, err := e.client.ImageInspect(ctx, ref.Reference)
	if err != nil || inspected.ID == "" {
		return errPull
	}
	return nil
}

func (e *Engine) Verify(ctx context.Context, value string, credential registry.Credential) error {
	ref, err := registry.ParseImage(value)
	if err != nil {
		return errVerify
	}
	auth, err := engineRegistryAuth(ref, credential)
	if err != nil {
		return errVerify
	}
	inspected, err := e.client.DistributionInspect(ctx, ref.Reference, auth)
	if err != nil || inspected.Descriptor.Digest.Validate() != nil || ref.Digest != "" && inspected.Descriptor.Digest.String() != ref.Digest {
		return errVerify
	}
	return nil
}
