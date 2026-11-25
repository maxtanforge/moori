package nodeops

import (
	"context"
	"errors"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/errdefs"

	"github.com/maxtanforge/moori/internal/core/nodeops"
)

func (r Runner) deleteImage(ctx context.Context, req nodeops.Request) (nodeops.Result, error) {
	rows, err := r.Docker.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nodeops.Result{}, dockerError(err)
	}
	for _, row := range rows {
		if row.ImageID == req.ImageID {
			return nodeops.Result{}, errors.New("Docker image is used by a container; remove the container first")
		}
	}
	// Docker rechecks references at removal, including containers created after
	// the list above. Never force deletion or prune parent images implicitly.
	_, err = r.Docker.ImageRemove(ctx, req.ImageID, image.RemoveOptions{Force: false, PruneChildren: false})
	if err != nil {
		return nodeops.Result{}, dockerMutationError(err)
	}
	return nodeops.Result{ImageID: req.ImageID, ImageRemovedAt: time.Now().UTC().Format(time.RFC3339Nano)}, nil
}

func (r Runner) tagImage(ctx context.Context, req nodeops.Request) (nodeops.Result, error) {
	tag, err := nodeops.TaggedImage(req.Image)
	if err != nil {
		return nodeops.Result{}, err
	}
	source, err := r.Docker.ImageInspect(ctx, req.ImageID)
	if err != nil {
		return nodeops.Result{}, dockerError(err)
	}
	if source.ID != req.ImageID {
		return nodeops.Result{}, errors.New("Docker image identity changed")
	}
	existing, err := r.Docker.ImageInspect(ctx, tag)
	if err != nil && !errdefs.IsNotFound(err) {
		return nodeops.Result{}, dockerError(err)
	}
	if err == nil && !nodeops.ValidImageID(existing.ID) {
		return nodeops.Result{}, errors.New("Docker did not return an image identity for the requested tag")
	}
	result := nodeops.Result{Image: tag, ImageID: source.ID, CurrentImageID: existing.ID}
	if req.Action == nodeops.ImageTagPreview {
		return result, nil
	}
	if existing.ID != *req.ExpectedImageID || existing.ID != "" && existing.ID != source.ID && !req.Overwrite {
		return nodeops.Result{}, errors.New("the image tag changed or is already in use; preview the tag again")
	}
	if existing.ID != source.ID {
		if err := r.Docker.ImageTag(ctx, source.ID, tag); err != nil {
			return nodeops.Result{}, dockerMutationError(err)
		}
	}
	return result, nil
}
