package nodeops

import (
	"context"
	"errors"

	"github.com/docker/docker/errdefs"

	"github.com/maxtanforge/moori/internal/core/nodeops"
	"github.com/maxtanforge/moori/internal/core/registry"
)

func (r Runner) releaseContainer(ctx context.Context, req nodeops.Request) (nodeops.Result, error) {
	pullCtx, cancel := context.WithTimeout(ctx, req.Action.Timeout()-nodeops.ContainerReplace.Timeout())
	defer cancel()
	old, err := r.Docker.ContainerInspect(pullCtx, req.Name)
	if err != nil {
		return nodeops.Result{}, dockerError(err)
	}
	if old.ContainerJSONBase == nil || old.Name != "/"+req.Name || old.Config == nil {
		return nodeops.Result{}, errors.New("the webhook container name could not be verified")
	}
	original, err := registry.ParseImage(old.Config.Image)
	desired, _ := nodeops.ContainerReleaseImage(req.Image) // validated by Run
	if err != nil || original.Host != desired.Host || original.Repository != desired.Repository {
		return nodeops.Result{}, errors.New("the container image is outside the webhook repository")
	}
	replacement := nodeops.Request{Action: nodeops.ContainerReplace, Target: old.ID, Edit: &nodeops.ContainerEdit{Image: req.Image}}
	if err := replacement.Validate(); err != nil {
		return nodeops.Result{}, err
	}
	if _, _, _, err := prepareReplacement(old, *replacement.Edit); err != nil {
		return nodeops.Result{}, err
	}
	if old.Config.Image == req.Image {
		return nodeops.Result{Container: &nodeops.Container{FullID: old.ID, Name: req.Name, Image: req.Image}}, nil
	}
	if _, err := r.Docker.ImageInspect(pullCtx, req.Image); err != nil {
		if !errdefs.IsNotFound(err) {
			return nodeops.Result{}, dockerError(err)
		}
		if r.PullImage == nil || r.PullImage(pullCtx, req.Image) != nil {
			return nodeops.Result{}, errors.New("could not pull the container release image through its registry")
		}
	}
	cancel()
	return r.replaceContainer(ctx, replacement, &old)
}
