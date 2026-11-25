package localinv

import "time"

type Host struct {
	CPUPercent           *float64 `json:"cpu_percent,omitempty"`
	cpuTotal, cpuIdle    uint64
	SampledAt            time.Time   `json:"sampled_at,omitempty"`
	Scope                string      `json:"scope,omitempty"`
	Hostname             string      `json:"hostname,omitempty"`
	OS                   string      `json:"os,omitempty"`
	Kernel               string      `json:"kernel,omitempty"`
	Architecture         string      `json:"architecture,omitempty"`
	CPUModel             string      `json:"cpu_model,omitempty"`
	CPUCount             int         `json:"cpu_count,omitempty"`
	MemoryTotalBytes     uint64      `json:"memory_total_bytes,omitempty"`
	MemoryAvailableBytes uint64      `json:"memory_available_bytes,omitempty"`
	UptimeSeconds        uint64      `json:"uptime_seconds,omitempty"`
	Vendor               string      `json:"vendor,omitempty"`
	Product              string      `json:"product,omitempty"`
	Interfaces           []Interface `json:"interfaces,omitempty"`
	Warning              string      `json:"warning,omitempty"`
}

type Engine struct {
	SampledAt      time.Time `json:"sampled_at,omitempty"`
	Version        string    `json:"version,omitempty"`
	APIVersion     string    `json:"api_version,omitempty"`
	Name           string    `json:"name,omitempty"`
	RootDir        string    `json:"root_dir,omitempty"`
	StorageDriver  string    `json:"storage_driver,omitempty"`
	LoggingDriver  string    `json:"logging_driver,omitempty"`
	CgroupDriver   string    `json:"cgroup_driver,omitempty"`
	CgroupVersion  string    `json:"cgroup_version,omitempty"`
	DefaultRuntime string    `json:"default_runtime,omitempty"`
}

type Interface struct {
	Name    string `json:"name,omitempty"`
	RxBytes uint64 `json:"rx_bytes"`
	TxBytes uint64 `json:"tx_bytes"`
}

type Port struct {
	IP       string `json:"ip,omitempty"`
	Private  uint16 `json:"private,omitempty"`
	Public   uint16 `json:"public,omitempty"`
	Protocol string `json:"protocol,omitempty"`
}

type Mount struct {
	Type        string `json:"type,omitempty"`
	Name        string `json:"name,omitempty"`
	Source      string `json:"source,omitempty"`
	Destination string `json:"destination,omitempty"`
	ReadOnly    bool   `json:"read_only"`
}

type Stats struct {
	systemCPU, totalCPU uint64
	SampledAt           time.Time `json:"sampled_at,omitempty"`
	CPUPercent          *float64  `json:"cpu_percent,omitempty"`
	MemoryBytes         uint64    `json:"memory_bytes"`
	MemoryLimitBytes    uint64    `json:"memory_limit_bytes"`
	PIDs                uint64    `json:"pids"`
	RxBytes             uint64    `json:"rx_bytes"`
	TxBytes             uint64    `json:"tx_bytes"`
	BlockReadBytes      uint64    `json:"block_read_bytes"`
	BlockWriteBytes     uint64    `json:"block_write_bytes"`
}

type ComposeProject struct {
	Name     string   `json:"name,omitempty"`
	Services []string `json:"services,omitempty"`
}
