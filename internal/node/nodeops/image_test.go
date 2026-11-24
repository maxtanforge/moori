package nodeops

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"

	"github.com/maxtanforge/moori/internal/core/nodeops"
)

type tagDocker struct {
	Docker
	images map[string]string
	tagged string
}

func (d *tagDocker) ImageInspect(_ context.Context, reference string, _ ...client.ImageInspectOption) (image.InspectResponse, error) {
	if id := d.images[reference]; id != "" {
		return image.InspectResponse{ID: id}, nil
	}
	return image.InspectResponse{}, errdefs.NotFound(errors.New("not found"))
}

func (d *tagDocker) ImageTag(_ context.Context, id, reference string) error {
	d.tagged = id
	d.images[reference] = id
	return nil
}

func TestImageTagDoesNotOverwriteConcurrentChange(t *testing.T) {
	id := "sha256:" + strings.Repeat("a", 64)
	occupied := "sha256:" + strings.Repeat("b", 64)
	destination := "registry.example/app:web"
	docker := &tagDocker{images: map[string]string{id: id, destination: occupied}}
	r := Runner{Docker: docker}
	makeRequest := func(action, expected string, overwrite bool) nodeops.Request {
		var req nodeops.Request
		body := `{"action":"` + action + `","image_id":"` + id + `","image":"` + destination + `"`
		if action == "image.tag" {
			body += `,"expected_image_id":"` + expected + `"`
		}
		if overwrite {
			body += `,"overwrite":true`
		}
		if err := json.Unmarshal([]byte(body+"}"), &req); err != nil {
			t.Fatal(err)
		}
		return req
	}
	ctx := context.Background()
	if _, err := r.Run(ctx, makeRequest("image.tag-preview", "", false)); err != nil {
		t.Fatalf("preview failed: %v", err)
	}
	if _, err := r.Run(ctx, makeRequest("image.tag", "", false)); err == nil || docker.tagged != "" {
		t.Fatal("replaced an unconfirmed occupied tag")
	}
	if _, err := r.Run(ctx, makeRequest("image.tag", occupied, true)); err != nil || docker.tagged != id {
		t.Fatalf("confirmed tag did not apply: %v", err)
	}
}
