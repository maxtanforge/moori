// Package nodeops defines the bounded node resource operation protocol.
package nodeops

import (
	"errors"
	"regexp"
	"time"

	"github.com/distribution/reference"
)

type Action string

const (
	ContainerInspect         Action = "container.inspect"
	ContainerEditConfig      Action = "container.edit-config"
	ContainerCreate          Action = "container.create"
	ContainerLogs            Action = "container.logs"
	ContainerStart           Action = "container.start"
	ContainerStop            Action = "container.stop"
	ContainerRestart         Action = "container.restart"
	ContainerPause           Action = "container.pause"
	ContainerResume          Action = "container.resume"
	ContainerRemove          Action = "container.remove"
	ContainerResourcesUpdate Action = "container.resources"
	ContainerReplace         Action = "container.replace"
	ContainerRelease         Action = "container.release"
	ComposeRemove            Action = "compose.remove"
	VolumeInspect            Action = "volume.inspect"
	VolumeCreate             Action = "volume.create"
	VolumeDelete             Action = "volume.delete"
	NetworkInspect           Action = "network.inspect"
	NetworkCreate            Action = "network.create"
	NetworkConnect           Action = "network.connect"
	NetworkDisconnect        Action = "network.disconnect"
	NetworkDelete            Action = "network.delete"
	RegistryVerify           Action = "registry.verify"
	RegistryPull             Action = "image.pull"
	ImageTagPreview          Action = "image.tag-preview"
	ImageTag                 Action = "image.tag"
	ImageDelete              Action = "image.delete"
	ImagePush                Action = "image.push"
	ImageExport              Action = "image.export"
	ImageImport              Action = "image.import"
)

const (
	MinLogLines     = 100
	MaxLogLines     = 1000
	DefaultLogLines = 100
	MaxLogBytes     = 64 << 10
	MaxResultBytes  = 512 << 10
	MaxComposeBytes = 256 << 10
)

var (
	ErrInvalid        = errors.New("invalid node operation")
	ErrOutcomeUnknown = errors.New("Docker did not confirm the result")
	ErrPullStalled    = errors.New("Docker stopped reporting image pull progress")
	resourceName      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	composeName       = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,127}$`)
	managedCompose    = regexp.MustCompile(`^moori-[0-9a-f]{32}$`)
	fullID            = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

type Request struct {
	Resources       *ContainerResources `json:"resources,omitempty"`
	TmpfsSizeMiB    int64               `json:"tmpfs_size_mib,omitempty"`
	ComposeSource   *ComposeSourceEdit  `json:"compose_source,omitempty"`
	Edit            *ContainerEdit      `json:"edit,omitempty"`
	ForceRecreate   bool                `json:"force_recreate,omitempty"`
	RemoveVolumes   bool                `json:"remove_volumes,omitempty"`
	Action          Action              `json:"action"`
	Target          string              `json:"target,omitempty"`
	Container       string              `json:"container,omitempty"`
	Name            string              `json:"name,omitempty"`
	Tail            int                 `json:"tail,omitempty"`
	Timestamps      bool                `json:"timestamps,omitempty"`
	AllLogRuns      bool                `json:"all_log_runs,omitempty"`
	Generation      int64               `json:"generation,omitempty"`
	RegistryID      string              `json:"registry_id,omitempty"`
	Project         string              `json:"project,omitempty"`
	Image           string              `json:"image,omitempty"`
	ImageID         string              `json:"image_id,omitempty"`
	ExpectedImageID *string             `json:"expected_image_id,omitempty"`
	Overwrite       bool                `json:"overwrite,omitempty"`
	Operation       string              `json:"-"`
}

func (r Request) Validate() error {
	if (r.Resources != nil) != (r.Action == ContainerResourcesUpdate) ||
		r.Resources != nil && (!r.Resources.valid() ||
			r.Resources.NanoCPUs != nil && *r.Resources.NanoCPUs == 0 ||
			r.Resources.MemoryBytes != nil && *r.Resources.MemoryBytes == 0) ||
		r.TmpfsSizeMiB != 0 && (r.Action != VolumeCreate || r.TmpfsSizeMiB < 1 || r.TmpfsSizeMiB > 1<<20) {
		return ErrInvalid
	}
	if r.RemoveVolumes && r.Action != ContainerRemove && r.Action != ComposeRemove {
		return ErrInvalid
	}
	if r.Action == ContainerRelease {
		return r.validateContainerRelease()
	}
	if r.Action.ComposeFileOperation() {
		return r.validateComposeFile()
	}
	if r.ComposeSource != nil {
		return ErrInvalid
	}
	if r.Action == ImageTagPreview || r.Action == ImageTag || r.Action == ImageDelete || r.Action == ImagePush || r.Action.Archive() {
		return r.validateImageOperation()
	}
	if r.ImageID != "" || r.ExpectedImageID != nil || r.Overwrite {
		return ErrInvalid
	}
	if (r.Edit != nil) != (r.Action == ContainerReplace || r.Action == ContainerCreate) ||
		r.Edit != nil && !r.Edit.valid() {
		return ErrInvalid
	}
	if r.ForceRecreate {
		return ErrInvalid
	}
	if r.Action.Registry() {
		if len(r.Image) == 0 || len(r.Image) > 512 || r.Container != "" || r.Name != "" || r.Tail != 0 ||
			r.RegistryID != "" && !resourceName.MatchString(r.RegistryID) ||
			r.Action == RegistryVerify && r.RegistryID == "" ||
			r.Project != "" && !resourceName.MatchString(r.Project) ||
			r.Target != "" && (r.Project == "" || !resourceName.MatchString(r.Target)) {
			return ErrInvalid
		}
		if _, err := reference.ParseNormalizedNamed(r.Image); err != nil {
			return ErrInvalid
		}
		return nil
	}
	if r.RegistryID != "" || r.Project != "" || r.Image != "" ||
		r.Action != ContainerLogs && (r.Tail != 0 || r.Timestamps || r.AllLogRuns) ||
		r.Action != VolumeCreate && r.Action != NetworkCreate && r.Action != ContainerCreate && r.Name != "" ||
		r.Action != NetworkConnect && r.Action != NetworkDisconnect && r.Container != "" {
		return ErrInvalid
	}
	switch r.Action {
	case ComposeRemove:
		if ValidExistingComposeName(r.Target) {
			return nil
		}
	case ContainerInspect, ContainerEditConfig, ContainerStart, ContainerStop, ContainerRestart, ContainerPause, ContainerResume, ContainerRemove, ContainerReplace, ContainerResourcesUpdate, NetworkInspect, NetworkDelete:
		if fullID.MatchString(r.Target) {
			return nil
		}
	case ContainerLogs:
		if fullID.MatchString(r.Target) && (r.Tail == 0 || r.Tail >= MinLogLines && r.Tail <= MaxLogLines) {
			return nil
		}
	case VolumeInspect, VolumeDelete:
		if resourceName.MatchString(r.Target) {
			return nil
		}
	case VolumeCreate, NetworkCreate:
		if r.Target == "" && resourceName.MatchString(r.Name) {
			return nil
		}
	case ContainerCreate:
		if r.Target == "" && len(r.Name) >= 2 && resourceName.MatchString(r.Name) && r.Edit.Image != "" {
			return nil
		}
	case NetworkConnect, NetworkDisconnect:
		if fullID.MatchString(r.Target) && fullID.MatchString(r.Container) {
			return nil
		}
	}
	return ErrInvalid
}

func ValidExistingComposeName(name string) bool {
	return composeName.MatchString(name) && !managedCompose.MatchString(name)
}

func (a Action) Registry() bool { return a == RegistryVerify || a == RegistryPull || a == ImagePush }

func (a Action) Archive() bool { return a == ImageExport || a == ImageImport }

// LocalDirect reports whether an idle local executor can serve this operation
// within the HTTP request.
func (a Action) LocalDirect() bool {
	switch a {
	case ContainerInspect, ContainerEditConfig, ContainerStart, ContainerStop, ContainerRestart, ContainerPause, ContainerResume,
		NetworkInspect, VolumeInspect, ComposeSourceRead, ContainerResourcesUpdate:
		return true
	default:
		return false
	}
}

func (a Action) Mutates() bool {
	switch a {
	case ContainerInspect, ContainerEditConfig, ContainerLogs, VolumeInspect, NetworkInspect, RegistryVerify, ImageTagPreview, ImageExport, ComposeSourceRead, ComposeSourcePreview:
		return false
	default:
		return true
	}
}

func (a Action) Timeout() time.Duration {
	if a == RegistryPull {
		return 6 * time.Hour
	}
	if a == ContainerCreate || a == ContainerRelease || a == ComposeSourceCreate || a == ImagePush || a.Archive() {
		return 15 * time.Minute
	}
	if a == ComposeRemove || a == ContainerReplace || a == ComposeSourceDeploy {
		return 3 * time.Minute
	}
	return 25 * time.Second
}
