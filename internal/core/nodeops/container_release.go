package nodeops

import (
	"github.com/maxtanforge/moori/internal/core/registry"
	"github.com/distribution/reference"
)

// ContainerRepository accepts an explicit, normalized registry repository.
func ContainerRepository(value string) bool {
	named, err := reference.ParseNormalizedNamed(value)
	return err == nil && len(value) <= 310 && reference.IsNameOnly(named) && named.String() == value
}

func ContainerReleaseImage(value string) (registry.Image, error) {
	image, err := registry.ParseImage(value)
	if err != nil || image.Digest == "" || image.Host+"/"+image.Repository+"@"+image.Digest != value {
		return registry.Image{}, ErrInvalid
	}
	return image, nil
}

func (r Request) validateContainerRelease() error {
	if _, err := ContainerReleaseImage(r.Image); err != nil {
		return err
	}
	if !resourceName.MatchString(r.Name) ||
		r.Project != "" && !resourceName.MatchString(r.Project) ||
		r.Target != "" && (r.Project == "" || !resourceName.MatchString(r.Target)) ||
		r.RegistryID != "" && !resourceName.MatchString(r.RegistryID) ||
		r.ComposeSource != nil || r.Edit != nil || r.ForceRecreate || r.Container != "" ||
		r.Tail != 0 || r.ImageID != "" || r.ExpectedImageID != nil || r.Overwrite {
		return ErrInvalid
	}
	return nil
}
