package nodeops

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/errdefs"

	"github.com/maxtanforge/moori/internal/core/nodeops"
)

type deleteImageDocker struct {
	Docker
	rows               []container.Summary
	listErr, removeErr error
	removed            string
}

func (d *deleteImageDocker) ContainerList(_ context.Context, options container.ListOptions) ([]container.Summary, error) {
	if !options.All {
		panic("must include stopped containers")
	}
	return d.rows, d.listErr
}

func (d *deleteImageDocker) ImageRemove(_ context.Context, id string, options image.RemoveOptions) ([]image.DeleteResponse, error) {
	if options.Force || options.PruneChildren {
		panic("unsafe removal options")
	}
	d.removed = id
	return nil, d.removeErr
}

func TestDeleteImageChecksLiveUsage(t *testing.T) {
	id := "sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name               string
		rows               []container.Summary
		listErr, removeErr error
		called, success    bool
	}{
		{name: "unused", called: true, success: true},
		{name: "unrelated container", rows: []container.Summary{{ImageID: "sha256:" + strings.Repeat("b", 64)}}, called: true, success: true},
		{name: "running", rows: []container.Summary{{ImageID: id, State: "running"}}},
		{name: "stopped", rows: []container.Summary{{ImageID: id, State: "exited"}}},
		{name: "unknown usage", listErr: errors.New("offline")},
		{name: "concurrent container creation", called: true, removeErr: errdefs.Conflict(errors.New("in use"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &deleteImageDocker{rows: tc.rows, listErr: tc.listErr, removeErr: tc.removeErr}
			result, err := (Runner{Docker: d}).Run(context.Background(), nodeops.Request{Action: nodeops.ImageDelete, ImageID: id})
			if (err == nil) != tc.success || (d.removed != "") != tc.called {
				t.Fatalf("removed=%q result=%+v err=%v", d.removed, result, err)
			}
			if tc.success && result.ImageID != id {
				t.Fatal(result)
			}
		})
	}
}
