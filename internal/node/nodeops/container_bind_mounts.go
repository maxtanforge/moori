package nodeops

import (
	"path"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
)

// prepareBindMounts preserves the complete options of an existing mount when
// its source and destination are unchanged. New mounts use structured inputs.
func prepareBindMounts(host *container.HostConfig, desired *[]string) {
	if desired == nil {
		return
	}
	edits := make([]mount.Mount, 0, len(*desired))
	byTarget := make(map[string]int, len(*desired))
	for _, spec := range *desired {
		parts := strings.Split(spec, ":")
		byTarget[parts[1]] = len(edits)
		edits = append(edits, mount.Mount{Type: mount.TypeBind, Source: parts[0], Target: parts[1], ReadOnly: len(parts) == 3 && parts[2] == "ro"})
	}
	binds := make([]string, 0, len(host.Binds))
	for _, bind := range host.Binds {
		parts := strings.SplitN(bind, ":", 3)
		if len(parts) < 2 || !path.IsAbs(parts[0]) {
			binds = append(binds, bind)
			continue
		}
		if i, ok := byTarget[path.Clean(parts[1])]; ok && edits[i].Source == parts[0] {
			binds = append(binds, strings.Join(retainVolumeBindMode(parts, edits[i].ReadOnly), ":"))
			edits[i].Target = ""
		}
	}
	mounts := make([]mount.Mount, 0, len(host.Mounts)+len(edits))
	for _, item := range host.Mounts {
		if item.Type != mount.TypeBind {
			mounts = append(mounts, item)
			continue
		}
		if i, ok := byTarget[path.Clean(item.Target)]; ok && item.Source == edits[i].Source {
			item.ReadOnly = edits[i].ReadOnly
			mounts = append(mounts, item)
			edits[i].Target = ""
		}
	}
	for _, item := range edits {
		if item.Target != "" {
			mounts = append(mounts, item)
		}
	}
	host.Binds, host.Mounts = binds, mounts
}

func duplicateMountTargets(host *container.HostConfig) bool {
	seen := make(map[string]bool)
	add := func(value string) bool {
		target := path.Clean(value)
		if seen[target] {
			return false
		}
		seen[target] = true
		return true
	}
	for _, spec := range host.Binds {
		parts := strings.SplitN(spec, ":", 3)
		target := parts[0]
		if len(parts) > 1 {
			target = parts[1]
		}
		if !add(target) {
			return true
		}
	}
	for _, item := range host.Mounts {
		if !add(item.Target) {
			return true
		}
	}
	for target := range host.Tmpfs {
		if !add(target) {
			return true
		}
	}
	return false
}
