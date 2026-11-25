package localinv

import (
	"context"
	"errors"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
)

type unavailableContainers struct{ *fakeDocker }

func (d unavailableContainers) ContainerList(context.Context, container.ListOptions) ([]container.Summary, error) {
	return nil, errors.New("unavailable")
}

func TestImageUsageCountsBeforeTruncation(t *testing.T) {
	d := &fakeDocker{images: []image.Summary{{ID: "used"}, {ID: "unused"}}}
	for j := 0; j < maxListedContainers+1; j++ {
		d.containers = append(d.containers, container.Summary{ID: "container", ImageID: "used", State: "exited"})
	}
	o, err := collect(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Containers) != maxListedContainers {
		t.Fatal(len(o.Containers))
	}
	for _, item := range o.Images {
		want := 0
		if item.FullID == "used" {
			want = maxListedContainers + 1
		}
		if item.ContainerCount == nil || *item.ContainerCount != want {
			t.Fatalf("%+v", item)
		}
	}
}

func TestImageUsageUnknownWhenContainerReadFails(t *testing.T) {
	d := unavailableContainers{&fakeDocker{images: []image.Summary{{ID: "image"}}}}
	o, _ := collect(context.Background(), d)
	if len(o.Images) != 1 || o.Images[0].ContainerCount != nil {
		t.Fatal(o.Images)
	}
}
