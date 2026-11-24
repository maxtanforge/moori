// Package management connects Docker resource actions to the execution node.
package management

import (
	"context"
	"errors"
	"time"

	"github.com/maxtanforge/moori/internal/core/nodeops"
	"github.com/maxtanforge/moori/internal/core/registry"
	"github.com/maxtanforge/moori/internal/node/execstream"
	dockerops "github.com/maxtanforge/moori/internal/node/nodeops"
	noderegistry "github.com/maxtanforge/moori/internal/node/registry"
	"github.com/maxtanforge/moori/internal/transport/imagearchive"
)

type Operations struct {
	sourceDocker string
	Archives     imagearchive.Endpoint
	docker       *dockerops.Runner
	images       *noderegistry.Engine
	credentials  func(context.Context, string) (registry.Credential, error)
}

func Open(images *noderegistry.Engine, credentials func(context.Context, string) (registry.Credential, error)) (*Operations, error) {
	docker, err := dockerops.NewLocal()
	if err != nil {
		return nil, err
	}
	return &Operations{docker: docker, images: images, credentials: credentials}, nil
}

func (o *Operations) Close() error { return o.docker.Close() }

func (o *Operations) TerminalStarter() execstream.Starter {
	cli, ok := o.docker.Docker.(execstream.DockerExec)
	if !ok {
		return nil
	}
	return execstream.DockerStarter(cli)
}

func (o *Operations) Run(ctx context.Context, req nodeops.Request) (nodeops.Result, error) {
	if req.Action.ComposeFileOperation() {
		return o.runComposeSource(ctx, req)
	}
	if req.Action.Archive() {
		return o.transferImage(ctx, req)
	}
	if req.Action == nodeops.ComposeRemove && req.RemoveVolumes {
		if _, err := o.runComposeSource(ctx, nodeops.Request{Action: nodeops.ComposeSourceRead, Target: req.Target}); err != nil {
			return nodeops.Result{}, err
		}
	}
	if _, image, ok := req.RegistryAccess(); ok && !req.Action.Registry() {
		runner := *o.docker
		runner.PullImage = func(ctx context.Context, ref string) error {
			if ref != image {
				return registry.ErrOperationCredentials
			}
			cred, err := o.operationCredential(ctx, req.Operation)
			if err != nil {
				return err
			}
			return o.images.Pull(ctx, ref, "", cred)
		}
		return runner.Run(ctx, req)
	}
	if !req.Action.Registry() {
		return o.docker.Run(ctx, req)
	}
	if err := req.Validate(); err != nil {
		return nodeops.Result{}, err
	}
	cred, err := o.operationCredential(ctx, req.Operation)
	if err != nil {
		return nodeops.Result{}, err
	}
	switch req.Action {
	case nodeops.RegistryVerify:
		err = o.images.Verify(ctx, req.Image, cred)
	case nodeops.RegistryPull:
		err = o.images.Pull(ctx, req.Image, "", cred)
	case nodeops.ImagePush:
		err = o.images.Push(ctx, req.Image, req.ImageID, cred)
	}
	if err != nil {
		return nodeops.Result{}, err
	}
	result := nodeops.Result{Image: req.Image, ImageID: req.ImageID}
	if req.Action == nodeops.RegistryPull {
		result.ResourceObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	return result, nil
}

func (o *Operations) operationCredential(ctx context.Context, operation string) (registry.Credential, error) {
	if o.images == nil || o.credentials == nil {
		return registry.Credential{}, registry.ErrOperationCredentials
	}
	cred, err := o.credentials(ctx, operation)
	switch {
	case err == nil:
		return cred, nil
	case errors.Is(err, registry.ErrAmbiguous):
		return registry.Credential{}, registry.ErrAmbiguous
	case errors.Is(err, registry.ErrSecureTransport):
		return registry.Credential{}, registry.ErrSecureTransport
	default:
		return registry.Credential{}, registry.ErrOperationCredentials
	}
}
