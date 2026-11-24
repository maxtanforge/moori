package hostmetrics

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"syscall"

	"github.com/maxtanforge/moori/internal/core/telemetry"
)

type Collector struct {
	dataPath string
	previous cpuCounters
}

func New(dataPath string) *Collector { return &Collector{dataPath: dataPath} }

func (c *Collector) Sample() (telemetry.Sample, error) {
	proc := "/proc"
	if root := os.Getenv("MOORI_HOST_ROOT"); root != "" {
		proc = filepath.Join(root, "proc")
	}
	mem, err := os.Open(filepath.Join(proc, "meminfo"))
	if err != nil {
		return telemetry.Sample{}, err
	}
	defer mem.Close()
	total, available, err := readMemory(mem)
	if err != nil {
		return telemetry.Sample{}, err
	}
	var fs syscall.Statfs_t
	if err := syscall.Statfs(c.dataPath, &fs); err != nil {
		return telemetry.Sample{}, err
	}
	if fs.Bsize <= 0 || fs.Blocks == 0 || fs.Blocks > math.MaxInt64/uint64(fs.Bsize) || fs.Bavail > fs.Blocks {
		return telemetry.Sample{}, errors.New("invalid filesystem capacity")
	}
	result := telemetry.Sample{MemoryBytes: total, MemoryAvailableBytes: available,
		DiskBytes: fs.Blocks * uint64(fs.Bsize), DiskAvailableBytes: fs.Bavail * uint64(fs.Bsize)}
	if file, err := os.Open(filepath.Join(proc, "stat")); err == nil {
		if current, err := readCPU(file); err == nil {
			result.CPU = current.utilization(c.previous)
			c.previous = current
		}
		file.Close()
	}
	return result, nil
}
