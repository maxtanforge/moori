package registry

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/docker/docker/api/types/image"
	dockerRegistry "github.com/docker/docker/api/types/registry"
	"github.com/docker/docker/client"
	"github.com/maxtanforge/moori/internal/core/nodeops"
	"github.com/maxtanforge/moori/internal/core/registry"
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
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	const idleTimeout = 15 * time.Minute
	timer := time.AfterFunc(idleTimeout, func() { cancel(nodeops.ErrPullStalled) })
	defer timer.Stop()
	body, err := e.client.ImagePull(ctx, ref.Reference, image.PullOptions{RegistryAuth: auth, Platform: platform})
	if err != nil {
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		return errPull
	}
	defer body.Close()
	const maxEventBytes = 1 << 20
	progress := &io.LimitedReader{R: body, N: maxEventBytes}
	decoder := json.NewDecoder(progress)
	received, finished := false, false
	for {
		var event struct {
			Status      string          `json:"status"`
			Error       string          `json:"error"`
			ErrorDetail json.RawMessage `json:"errorDetail"`
		}
		err := decoder.Decode(&event)
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		if err == io.EOF && received && progress.N > 0 {
			// An existing mutable tag also passes ImageInspect if the progress
			// stream closes at a frame boundary halfway through its update.
			if ref.Digest == "" && !finished {
				return nodeops.ErrOutcomeUnknown
			}
			break
		}
		if err != nil {
			return nodeops.ErrOutcomeUnknown
		}
		if event.Error != "" || len(event.ErrorDetail) > 0 && string(event.ErrorDetail) != "null" {
			return errPull
		}
		progress.N = maxEventBytes
		received = true
		if strings.HasPrefix(event.Status, "Status: Downloaded newer image for ") || strings.HasPrefix(event.Status, "Status: Image is up to date for ") {
			finished = true
		}
		timer.Reset(idleTimeout)
	}
	// The workload can only start after Docker has persisted the exact reference.
	inspected, err := e.client.ImageInspect(ctx, ref.Reference)
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	if err != nil || inspected.ID == "" {
		return nodeops.ErrOutcomeUnknown
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
