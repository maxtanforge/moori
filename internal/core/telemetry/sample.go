// Package telemetry defines optional host resource samples.
package telemetry

import "math"

type Sample struct {
	CPU                  *uint16 `json:"cpu_basis_points,omitempty"`
	MemoryBytes          uint64  `json:"memory_bytes"`
	MemoryAvailableBytes uint64  `json:"memory_available_bytes"`
	DiskBytes            uint64  `json:"disk_bytes"`
	DiskAvailableBytes   uint64  `json:"disk_available_bytes"`
}

func (s Sample) Valid() bool {
	return (s.CPU == nil || *s.CPU <= 10000) &&
		s.MemoryBytes > 0 && s.MemoryBytes <= math.MaxInt64 && s.MemoryAvailableBytes <= s.MemoryBytes &&
		s.DiskBytes > 0 && s.DiskBytes <= math.MaxInt64 && s.DiskAvailableBytes <= s.DiskBytes
}
