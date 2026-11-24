package nodeops

import (
	"strings"

	"github.com/distribution/reference"
)

func ValidImageID(id string) bool {
	raw, ok := strings.CutPrefix(id, "sha256:")
	return ok && fullID.MatchString(raw)
}

func TaggedImage(value string) (string, error) {
	if len(value) > 384 {
		return "", ErrInvalid
	}
	named, err := reference.ParseNormalizedNamed(value)
	if err != nil {
		return "", ErrInvalid
	}
	if _, digest := named.(reference.Digested); digest || reference.IsNameOnly(named) {
		return "", ErrInvalid
	}
	return named.String(), nil
}

func (r Request) validateImageOperation() error {
	if r.Edit != nil || r.ForceRecreate || r.Container != "" || r.Name != "" || r.Tail != 0 {
		return ErrInvalid
	}
	if r.Action == ImageDelete {
		if ValidImageID(r.ImageID) && r.RegistryID == "" && r.Project == "" && r.Target == "" && r.Image == "" && r.ExpectedImageID == nil && !r.Overwrite {
			return nil
		}
		return ErrInvalid
	}
	if r.Action.Archive() {
		if r.RegistryID != "" || r.Project != "" || r.Target != "" || r.Image != "" || r.ExpectedImageID != nil || r.Overwrite {
			return ErrInvalid
		}
		if r.Action == ImageExport && ValidImageID(r.ImageID) || r.Action == ImageImport && r.ImageID == "" {
			return nil
		}
		return ErrInvalid
	}
	if !ValidImageID(r.ImageID) {
		return ErrInvalid
	}
	if _, err := TaggedImage(r.Image); err != nil {
		return err
	}
	if r.Action == ImagePush {
		if !resourceName.MatchString(r.RegistryID) || r.Project != "" && !resourceName.MatchString(r.Project) ||
			r.Target != "" && (r.Project == "" || !resourceName.MatchString(r.Target)) || r.ExpectedImageID != nil || r.Overwrite {
			return ErrInvalid
		}
		return nil
	}
	if r.RegistryID != "" || r.Project != "" || r.Target != "" {
		return ErrInvalid
	}
	if r.Action == ImageTagPreview && r.ExpectedImageID == nil && !r.Overwrite {
		return nil
	}
	if r.Action == ImageTag && r.ExpectedImageID != nil && (*r.ExpectedImageID == "" || ValidImageID(*r.ExpectedImageID)) {
		return nil
	}
	return ErrInvalid
}
