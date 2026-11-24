package nodeops

import (
	"encoding/json"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/maxtanforge/moori/internal/core/registry"
	"github.com/distribution/reference"
	"github.com/docker/go-connections/nat"
)

type ContainerResources struct {
	NanoCPUs    *int64 `json:"nano_cpus,omitempty"`
	MemoryBytes *int64 `json:"memory_bytes,omitempty"`
	PIDs        *int64 `json:"pids,omitempty"`
}

func (r ContainerResources) valid() bool {
	return (r.NanoCPUs != nil || r.MemoryBytes != nil || r.PIDs != nil) &&
		(r.NanoCPUs == nil || *r.NanoCPUs == 0 || *r.NanoCPUs >= 10_000_000 && *r.NanoCPUs <= 1_000_000_000_000_000) &&
		(r.MemoryBytes == nil || *r.MemoryBytes == 0 || *r.MemoryBytes >= 6<<20 && *r.MemoryBytes <= 1<<50) &&
		(r.PIDs == nil || *r.PIDs >= 0 && *r.PIDs <= 1<<31-1)
}

type RegistryScope struct {
	RegistryID string `json:"registry_id"`
	Project    string `json:"project,omitempty"`
	Target     string `json:"target,omitempty"`
}

// RegistryAccess identifies the image and any explicit registry choice for an operation.
// A replacement's top-level target identifies the container, not the deployment target.
func (r Request) RegistryAccess() (RegistryScope, string, bool) {
	switch {
	case r.Action.Registry() || r.Action == ContainerRelease:
		return RegistryScope{RegistryID: r.RegistryID, Project: r.Project, Target: r.Target}, r.Image, true
	case (r.Action == ContainerReplace || r.Action == ContainerCreate) && r.Edit != nil && PullableContainerImage(r.Edit.Image):
		if r.Edit.PullScope != nil {
			return *r.Edit.PullScope, r.Edit.Image, true
		}
		return RegistryScope{}, r.Edit.Image, true
	default:
		return RegistryScope{}, "", false
	}
}

type ContainerEdit struct {
	PullScope   *RegistryScope      `json:"pull_scope,omitempty"`
	Resources   *ContainerResources `json:"resources,omitempty"`
	Image       string              `json:"image,omitempty"`
	Restart     *string             `json:"restart,omitempty"`
	Ports       *[]string           `json:"ports,omitempty"`
	Volumes     *[]string           `json:"volumes,omitempty"`
	Binds       *[]string           `json:"binds,omitempty"`
	Networks    *[]string           `json:"networks,omitempty"`
	Environment map[string]*string  `json:"environment,omitempty"`
	Labels      map[string]*string  `json:"labels,omitempty"`
}

var localImageID = regexp.MustCompile(`^(sha256:)?[a-f0-9]{12,64}$`)

// PullableContainerImage excludes local IDs, including Docker's abbreviated ID display.
func PullableContainerImage(image string) bool {
	if image == "" || localImageID.MatchString(image) {
		return false
	}
	_, err := registry.ParseImage(image)
	return err == nil
}

var envKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func RestartPolicy(value string) (name string, retries int, ok bool) {
	switch value {
	case "no", "always", "unless-stopped", "on-failure":
		return value, 0, true
	}
	retry, found := strings.CutPrefix(value, "on-failure:")
	n, err := strconv.ParseUint(retry, 10, 31)
	if !found || err != nil || strconv.FormatUint(n, 10) != retry {
		return "", 0, false
	}
	return "on-failure", int(n), true
}

func (e ContainerEdit) valid() bool {
	content, err := json.Marshal(e)
	if err != nil || len(content) > 32<<10 || e.Resources != nil && !e.Resources.valid() {
		return false
	}
	if scope := e.PullScope; scope != nil {
		if !resourceName.MatchString(scope.RegistryID) || scope.Project != "" && !resourceName.MatchString(scope.Project) ||
			scope.Target != "" && (scope.Project == "" || !resourceName.MatchString(scope.Target)) || !PullableContainerImage(e.Image) {
			return false
		}
	}
	if e.Image != "" {
		if _, err := reference.ParseAnyReference(e.Image); err != nil {
			return false
		}
	}
	if e.Restart != nil {
		if _, _, ok := RestartPolicy(*e.Restart); !ok {
			return false
		}
	}
	if e.Ports != nil {
		if len(*e.Ports) > 64 {
			return false
		}
		for _, spec := range *e.Ports {
			if len(spec) > 128 || strings.Contains(spec, "-") {
				return false
			}
		}
		if _, bindings, err := nat.ParsePortSpecs(*e.Ports); err != nil || len(bindings) > 128 {
			return false
		}
	}
	seenMounts := make(map[string]bool)
	if e.Volumes != nil {
		if len(*e.Volumes) > 64 {
			return false
		}
		for _, mount := range *e.Volumes {
			parts := strings.Split(mount, ":")
			if len(parts) < 2 || len(parts) > 3 || !resourceName.MatchString(parts[0]) {
				return false
			}
			dest := parts[1]
			if !path.IsAbs(dest) || dest == "/" || path.Clean(dest) != dest || strings.ContainsAny(dest, "\x00\r\n\\") || seenMounts[dest] ||
				len(parts) == 3 && parts[2] != "ro" && parts[2] != "rw" {
				return false
			}
			seenMounts[dest] = true
		}
	}
	if e.Binds != nil {
		if len(*e.Binds) > 64 {
			return false
		}
		for _, spec := range *e.Binds {
			parts := strings.Split(spec, ":")
			if len(parts) < 2 || len(parts) > 3 || !cleanBindPath(parts[0]) ||
				!cleanBindPath(parts[1]) || parts[1] == "/" || seenMounts[parts[1]] ||
				len(parts) == 3 && parts[2] != "ro" && parts[2] != "rw" {
				return false
			}
			seenMounts[parts[1]] = true
		}
	}
	if e.Networks != nil {
		if len(*e.Networks) > 64 {
			return false
		}
		seen := make(map[string]bool)
		for _, name := range *e.Networks {
			if !resourceName.MatchString(name) || name == "host" || name == "none" || seen[name] {
				return false
			}
			seen[name] = true
		}
	}
	for name, value := range e.Environment {
		if !envKey.MatchString(name) || value != nil && strings.ContainsRune(*value, 0) {
			return false
		}
	}
	for name, value := range e.Labels {
		if name == "" || strings.ContainsAny(name, " =\t\r\n\x00") ||
			strings.HasPrefix(strings.ToLower(name), "com.docker.") || value != nil && strings.ContainsRune(*value, 0) {
			return false
		}
	}
	return true
}

func cleanBindPath(value string) bool {
	return path.IsAbs(value) && path.Clean(value) == value && !strings.ContainsAny(value, "\x00\r\n\t\\,")
}
