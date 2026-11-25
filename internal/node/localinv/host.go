package localinv

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Read only known host inventory files, following symlinks within the mount.
func hostText(root *os.Root, path string, limit int64) string {
	text, _ := readHostText(root, path, limit)
	return text
}

func readHostText(root *os.Root, path string, limit int64) (string, error) {
	f, err := root.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func sampleHost(now time.Time, previous *Host) (*Host, []Filesystem, bool, error) {
	dir := os.Getenv("MOORI_HOST_ROOT")
	if dir == "" && runtime.GOOS != "linux" {
		name, _ := os.Hostname()
		return &Host{SampledAt: now, Scope: "process", Hostname: name, OS: runtime.GOOS, Architecture: runtime.GOARCH, CPUCount: runtime.NumCPU(), Warning: "仅提供当前进程所在系统的基本信息"}, nil, false, nil
	}
	external := dir != "" && dir != "/"
	if dir == "" {
		dir = "/"
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, nil, false, fmt.Errorf("无法打开主机目录 %s，请核对采集容器的挂载：%w", dir, err)
	}
	defer root.Close()
	h := &Host{SampledAt: now, Scope: "host", Architecture: runtime.GOARCH}
	if !external {
		if _, err := os.Stat("/.dockerenv"); err == nil {
			h.Scope = "container"
			h.Warning = "缺少宿主机信息挂载，部分数据来自容器命名空间"
		} else if _, err := os.Stat("/run/.containerenv"); err == nil {
			h.Scope = "container"
			h.Warning = "缺少宿主机信息挂载，部分数据来自容器命名空间"
		}
	}
	h.Hostname = hostText(root, "etc/hostname", 1024)
	h.Kernel = hostText(root, "proc/sys/kernel/osrelease", 1024)
	if h.Hostname == "" && !external {
		h.Hostname, _ = os.Hostname()
	}
	h.OS = releaseName(hostText(root, "etc/os-release", 64<<10))
	if h.OS == "" {
		h.OS = releaseName(hostText(root, "usr/lib/os-release", 64<<10))
	}
	h.cpuTotal, h.cpuIdle = cpuTicks(hostText(root, "proc/stat", 4<<20))
	if previous != nil && previous.Scope == h.Scope && previous.Hostname == h.Hostname && previous.cpuTotal > 0 && h.cpuTotal > previous.cpuTotal && h.cpuIdle >= previous.cpuIdle {
		total, idle := h.cpuTotal-previous.cpuTotal, h.cpuIdle-previous.cpuIdle
		if idle <= total {
			pct := 100 * float64(total-idle) / float64(total)
			h.CPUPercent = &pct
		}
	}
	cpu := hostText(root, "proc/cpuinfo", 4<<20)
	for line := range strings.SplitSeq(cpu, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "processor":
			h.CPUCount++
		case "model name", "Hardware":
			if h.CPUModel == "" {
				h.CPUModel = strings.TrimSpace(v)
			}
		}
	}
	mem, memErr := readHostText(root, "proc/meminfo", 64<<10)
	if mem == "" && cpu == "" && h.Kernel == "" {
		if memErr != nil {
			return nil, nil, false, fmt.Errorf("无法读取 %s 下的 proc/meminfo，请核对 proc 挂载：%w", dir, memErr)
		}
		return nil, nil, false, fmt.Errorf("%s 下的 proc/meminfo、proc/cpuinfo 和内核版本均无可读内容", dir)
	}
	for line := range strings.SplitSeq(mem, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		v, _ := strconv.ParseUint(f[1], 10, 64)
		switch f[0] {
		case "MemTotal:":
			h.MemoryTotalBytes = v * 1024
		case "MemAvailable:":
			h.MemoryAvailableBytes = v * 1024
		}
	}
	if f := strings.Fields(hostText(root, "proc/uptime", 1024)); len(f) > 0 {
		s, _ := strconv.ParseFloat(f[0], 64)
		if s > 0 {
			h.UptimeSeconds = uint64(s)
		}
	}
	h.Vendor = hostText(root, "sys/class/dmi/id/sys_vendor", 1024)
	h.Product = hostText(root, "sys/class/dmi/id/product_name", 1024)
	netPath := "proc/1/net/dev"
	if !external {
		netPath = "proc/net/dev"
	}
	net := hostText(root, netPath, 1<<20)
	for line := range strings.SplitSeq(net, "\n") {
		n, c, ok := strings.Cut(line, ":")
		f := strings.Fields(c)
		if !ok || len(f) < 16 {
			continue
		}
		rx, e1 := strconv.ParseUint(f[0], 10, 64)
		tx, e2 := strconv.ParseUint(f[8], 10, 64)
		if e1 == nil && e2 == nil {
			h.Interfaces = append(h.Interfaces, Interface{Name: strings.TrimSpace(n), RxBytes: rx, TxBytes: tx})
		}
	}
	if mem == "" || cpu == "" || net == "" {
		if h.Warning != "" {
			h.Warning += "；"
		}
		h.Warning += "部分 /proc 数据不可用"
	}
	fs, ok := hostFilesystems(root, external)
	return h, fs, ok, nil
}
func releaseName(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		if value, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			return strings.Trim(value, "'\"")
		}
	}
	return ""
}
func cpuTicks(stat string) (uint64, uint64) {
	line, _, _ := strings.Cut(stat, "\n")
	f := strings.Fields(line)
	if len(f) < 5 || f[0] != "cpu" {
		return 0, 0
	}
	var total, idle uint64
	// guest and guest_nice are already included in user and nice.
	for idx, s := range f[1:min(9, len(f))] {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return 0, 0
		}
		total += v
		if idx == 3 || idx == 4 {
			idle += v
		}
	}
	return total, idle
}
