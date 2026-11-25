package management

import (
	"context"
	"errors"
	"io"

	"github.com/maxtanforge/moori/internal/core/nodeops"
)

func (o *Operations) transferImage(ctx context.Context, req nodeops.Request) (nodeops.Result, error) {
	if err := req.Validate(); err != nil {
		return nodeops.Result{}, err
	}
	if o.Archives == nil || o.images == nil {
		return nodeops.Result{}, errors.New("image archives require local execution or a verified WSS Agent endpoint")
	}
	if req.Action == nodeops.ImageExport {
		body, err := o.images.Save(ctx, req.ImageID)
		if err != nil {
			return nodeops.Result{}, err
		}
		if err := o.Archives.Send(ctx, req.Operation, body); err != nil {
			return nodeops.Result{}, err
		}
		return nodeops.Result{ImageID: req.ImageID}, nil
	}

	var images []string
	started := false
	err := o.Archives.Receive(ctx, req.Operation, func(archive io.Reader) error {
		started = true
		var loadErr error
		images, loadErr = o.images.Load(ctx, archive)
		return loadErr
	})
	if err != nil {
		if started {
			return nodeops.Result{}, nodeops.ErrOutcomeUnknown
		}
		return nodeops.Result{}, err
	}
	return nodeops.Result{Images: images}, nil
}
