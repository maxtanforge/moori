//go:build linux

package localinv

import (
	"math"
	"os"

	"strconv"

	"sort"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

var mountsPath = "/proc/self/mounts"

// pseudoFilesystems are kernel virtual mounts that carry no real disk usage.
var pseudoFilesystems = map[string]bool{
	"proc": true, "sysfs": true, "devtmpfs": true, "devpts": true, "tmpfs": true,
	"ramfs": true, "mqueue": true, "nsfs": true, "overlay": true, "squashfs": true,
	"cgroup": true, "cgroup2": true, "cgroupfs": true, "tracefs": true, "debugfs": true,
	"configfs": true, "pstore": true, "bpf": true, "autofs": true, "securityfs": true,
	"hugetlbfs": true, "efivarfs": true, "binfmt_misc": true, "rpc_pipefs": true,
	"selinuxfs": true, "fusectl": true, "dev": true,
}

func readFilesystems(content []byte) []Filesystem {
	return statFilesystems("/", parseMounts(content), nil)
}

type mount struct{ device, mountpoint, fstype string }

// parseMounts keeps real filesystem mounts, reports each backing device once
// at its shortest mountpoint, and drops repeated mountpoints from stacked
// mounts. Entries are ordered by mountpoint length so the device dedupe keeps
// the canonical path.
func parseMounts(content []byte) []mount {
	var mounts []mount
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || !strings.HasPrefix(fields[1], "/") || pseudoFilesystems[fields[2]] {
			continue
		}
		mounts = append(mounts, mount{device: fields[0], mountpoint: unescapeMount(fields[1]), fstype: fields[2]})
	}
	sort.Slice(mounts, func(a, b int) bool {
		if len(mounts[a].mountpoint) != len(mounts[b].mountpoint) {
			return len(mounts[a].mountpoint) < len(mounts[b].mountpoint)
		}
		return mounts[a].mountpoint < mounts[b].mountpoint
	})
	seenDevice, seenPath := map[string]bool{}, map[string]bool{}
	var kept []mount
	for _, m := range mounts {
		if m.device != "" && m.device != "none" {
			if seenDevice[m.device] {
				continue
			}
			seenDevice[m.device] = true
		}
		if seenPath[m.mountpoint] {
			continue
		}
		seenPath[m.mountpoint] = true
		kept = append(kept, m)
	}
	sort.Slice(kept, func(a, b int) bool { return kept[a].mountpoint < kept[b].mountpoint })
	return kept
}

func statFilesystems(root string, mounts []mount, devices map[string]uint64) []Filesystem {
	var systems []Filesystem
	for _, m := range mounts {
		var stat syscall.Statfs_t
		expected, needMatch := devices[m.mountpoint]
		okStat := readDiskStat(root, m.mountpoint, expected, needMatch, &stat)
		if !okStat && needMatch {
			if data := os.Getenv("MOORI_DATA_DIR"); data != "" {
				okStat = readDiskStat(data, "/", expected, true, &stat)
			}
		}
		if !okStat {
			if needMatch {
				systems = append(systems, Filesystem{Mountpoint: m.mountpoint, Device: m.device, Filesystem: m.fstype, UsageUnavailable: true})
			}
			continue
		}
		if stat.Bsize <= 0 || stat.Blocks == 0 || stat.Blocks > math.MaxInt64/uint64(stat.Bsize) || stat.Bavail > stat.Blocks {
			continue
		}
		total := stat.Blocks * uint64(stat.Bsize)
		available := stat.Bavail * uint64(stat.Bsize)
		used := (stat.Blocks - stat.Bfree) * uint64(stat.Bsize)
		percent := 0.0
		if used+available > 0 {
			percent = math.Round(float64(used)/float64(used+available)*1000) / 10
		}
		systems = append(systems, Filesystem{
			Mountpoint: m.mountpoint, Device: m.device, Filesystem: m.fstype,
			TotalBytes: total, UsedBytes: used, AvailBytes: available, UsedPercent: percent,
		})
	}
	sort.Slice(systems, func(a, b int) bool { return systems[a].Mountpoint < systems[b].Mountpoint })
	return systems
}

// Open directory descriptors inside the selected root; proc magic links
// and absolute symlinks escaping it cannot redirect the statfs probe.
func readDiskStat(root, path string, expected uint64, match bool, stat *syscall.Statfs_t) bool {
	dir, err := os.OpenRoot(root)
	if err != nil {
		return false
	}
	defer dir.Close()
	name := strings.TrimPrefix(path, "/")
	if name == "" {
		name = "."
	}
	// O_PATH inspects filesystem metadata without reading a bind-mounted file.
	f, err := dir.OpenFile(name, unix.O_PATH, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	fd := int(f.Fd())
	var id syscall.Stat_t
	return (!match || syscall.Fstat(fd, &id) == nil && id.Dev == expected) && syscall.Fstatfs(fd, stat) == nil
}

func unescapeMount(s string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, "\\").Replace(s)
}

func hostFilesystems(root *os.Root, external bool) ([]Filesystem, bool) {
	path := "proc/self/mountinfo"
	if external {
		path = "proc/1/mountinfo"
	}
	info := hostText(root, path, 4<<20)
	if info == "" {
		return nil, false
	}
	// Check the backing device before statfs: a non-recursive host bind may
	// have the mountpoint directory but expose the underlying root filesystem.
	var lines strings.Builder
	devices := map[string]uint64{}
	for line := range strings.SplitSeq(info, "\n") {
		left, right, ok := strings.Cut(line, " - ")
		a, b := strings.Fields(left), strings.Fields(right)
		if !ok || len(a) < 5 || len(b) < 2 {
			continue
		}
		ma, mi, ok := strings.Cut(a[2], ":")
		major, e1 := strconv.ParseUint(ma, 10, 32)
		minor, e2 := strconv.ParseUint(mi, 10, 32)
		if !ok || e1 != nil || e2 != nil {
			continue
		}
		devices[unescapeMount(a[4])] = unix.Mkdev(uint32(major), uint32(minor))
		lines.WriteString(b[1] + " " + a[4] + " " + b[0] + "\n")
	}
	return statFilesystems(root.Name(), parseMounts([]byte(lines.String())), devices), true
}
