package registry

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"

	"github.com/maxtanforge/moori/internal/core/nodeops"
	"github.com/maxtanforge/moori/internal/core/registry"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
)

func (e *Engine) Push(ctx context.Context, value, imageID string, credential registry.Credential) error {
	tag, err := nodeops.TaggedImage(value)
	if err != nil || !nodeops.ValidImageID(imageID) {
		return nodeops.ErrInvalid
	}
	ref, err := registry.ParseImage(tag)
	if err != nil {
		return nodeops.ErrInvalid
	}
	auth, err := engineRegistryAuth(ref, credential)
	if err != nil {
		return err
	}
	current, err := e.client.ImageInspect(ctx, tag)
	if err != nil || current.ID != imageID {
		return errors.New("the selected tag no longer refers to the selected image")
	}
	body, err := e.client.ImagePush(ctx, tag, image.PushOptions{RegistryAuth: auth})
	if err != nil {
		// A request may fail after the daemon or registry commits the push.
		return nodeops.ErrOutcomeUnknown
	}
	defer body.Close()
	return imageTransferOutput(body, nil)
}

func (e *Engine) Save(ctx context.Context, imageID string) (io.ReadCloser, error) {
	if !nodeops.ValidImageID(imageID) {
		return nil, nodeops.ErrInvalid
	}
	current, err := e.client.ImageInspect(ctx, imageID)
	if err != nil || current.ID != imageID {
		return nil, errors.New("the selected image is no longer available")
	}
	body, err := e.client.ImageSave(ctx, []string{imageID})
	if err != nil {
		return nil, errors.New("Docker could not save the requested image")
	}
	return body, nil
}

func (e *Engine) Load(ctx context.Context, archive io.Reader) ([]string, error) {
	result, err := e.client.ImageLoad(ctx, archive, client.ImageLoadWithQuiet(true))
	if err != nil {
		return nil, nodeops.ErrOutcomeUnknown
	}
	defer result.Body.Close()
	var imported []string
	err = imageTransferOutput(result.Body, func(line string) {
		for _, prefix := range []string{"Loaded image: ", "Loaded image ID: "} {
			value, ok := strings.CutPrefix(strings.TrimSpace(line), prefix)
			if ok && len(imported) < 128 {
				tag, tagErr := nodeops.TaggedImage(value)
				if nodeops.ValidImageID(value) {
					imported = append(imported, value)
				} else if tagErr == nil {
					imported = append(imported, tag)
				}
			}
		}
	})
	slices.Sort(imported)
	return slices.Compact(imported), err
}

func imageTransferOutput(body io.Reader, stream func(string)) error {
	progress := &io.LimitedReader{R: body, N: 32 << 20}
	decoder := json.NewDecoder(progress)
	for {
		var event struct {
			Error       string
			ErrorDetail json.RawMessage
			Stream      string
		}
		err := decoder.Decode(&event)
		if err == io.EOF && progress.N > 0 {
			return nil
		}
		if err != nil {
			return nodeops.ErrOutcomeUnknown
		}
		if event.Error != "" || len(event.ErrorDetail) > 0 && string(event.ErrorDetail) != "null" {
			return errors.New("Docker did not complete the image transfer")
		}
		if stream != nil {
			stream(event.Stream)
		}
	}
}
