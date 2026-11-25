package nodeops

type State string

const (
	Queued    State = "queued"
	Running   State = "running"
	Succeeded State = "succeeded"
	Failed    State = "failed"
	Uncertain State = "uncertain"
)

// Result lists the supported data explicitly without serializing raw Docker
// inspections. Only the dedicated edit-config action returns EditConfig.
type Result struct {
	ResourceObservedAt string                     `json:"resource_observed_at,omitempty"`
	ComposeSource      *ComposeSourceResult       `json:"compose_source,omitempty"`
	EditConfig         *ContainerEditConfigResult `json:"edit_config,omitempty"`
	Container          *Container                 `json:"container,omitempty"`
	Volume             *Volume                    `json:"volume,omitempty"`
	Network            *Network                   `json:"network,omitempty"`
	Log                string                     `json:"log,omitempty"`
	Truncated          bool                       `json:"truncated,omitempty"`
	Image              string                     `json:"image,omitempty"`
	ImageID            string                     `json:"image_id,omitempty"`
	ImageRemovedAt     string                     `json:"image_removed_at,omitempty"`
	CurrentImageID     string                     `json:"current_image_id,omitempty"`
	Images             []string                   `json:"images,omitempty"`
}

const MaxContainerEditConfigBytes = 64 << 10

type ContainerEditConfigResult struct {
	Resources   *ContainerResources `json:"resources,omitempty"`
	LegacyCPU   bool                `json:"legacy_cpu,omitempty"`
	Environment map[string]*string  `json:"environment"`
	Labels      map[string]string   `json:"labels"`
}

type Operation struct {
	ID         string  `json:"id"`
	Agent      string  `json:"agent"`
	Generation int64   `json:"generation"`
	Action     Action  `json:"action"`
	State      State   `json:"state"`
	Error      string  `json:"error,omitempty"`
	Result     *Result `json:"result,omitempty"`
}

type Reference struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	State       string `json:"state,omitempty"`
	IPv4Address string `json:"ipv4_address,omitempty"`
	IPv6Address string `json:"ipv6_address,omitempty"`
}

type Container struct {
	PIDsLimit        int64    `json:"pids_limit,omitempty"`
	ID               string   `json:"id"`
	FullID           string   `json:"full_id"`
	Name             string   `json:"name"`
	Image            string   `json:"image,omitempty"`
	ImageID          string   `json:"image_id,omitempty"`
	State            string   `json:"state,omitempty"`
	Status           string   `json:"status,omitempty"`
	Health           string   `json:"health,omitempty"`
	StateObservedAt  string   `json:"state_observed_at,omitempty"`
	RestartPolicy    string   `json:"restart_policy,omitempty"`
	MemoryLimitBytes int64    `json:"memory_limit_bytes,omitempty"`
	NanoCPUs         int64    `json:"nano_cpus,omitempty"`
	ComposeProject   string   `json:"compose_project,omitempty"`
	ComposeService   string   `json:"compose_service,omitempty"`
	StartedAt        string   `json:"started_at,omitempty"`
	FinishedAt       string   `json:"finished_at,omitempty"`
	ExitCode         *int     `json:"exit_code,omitempty"`
	RestartCount     int      `json:"restart_count,omitempty"`
	Ports            []Port   `json:"ports,omitempty"`
	Mounts           []Mount  `json:"mounts,omitempty"`
	Networks         []string `json:"networks,omitempty"`
}

type Port struct {
	IP       string `json:"ip,omitempty"`
	Private  uint16 `json:"private"`
	Public   uint16 `json:"public,omitempty"`
	Protocol string `json:"protocol"`
}

type Mount struct {
	Type        string `json:"type"`
	Name        string `json:"name,omitempty"`
	Source      string `json:"source,omitempty"`
	Destination string `json:"destination"`
	ReadOnly    bool   `json:"read_only"`
}

type Network struct {
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	Driver     string      `json:"driver"`
	Scope      string      `json:"scope"`
	Internal   bool        `json:"internal"`
	Attachable bool        `json:"attachable"`
	Subnets    []string    `json:"subnets,omitempty"`
	Gateways   []string    `json:"gateways,omitempty"`
	References []Reference `json:"references"`
	RefCount   int         `json:"ref_count"`
	Protected  bool        `json:"protected"`
}

type Volume struct {
	Tmpfs         bool        `json:"tmpfs,omitempty"`
	CapacityBytes int64       `json:"capacity_bytes,omitempty"`
	Name          string      `json:"name"`
	Driver        string      `json:"driver"`
	Scope         string      `json:"scope"`
	Mountpoint    string      `json:"mountpoint,omitempty"`
	CreatedAt     string      `json:"created_at,omitempty"`
	References    []Reference `json:"references"`
	RefCount      int         `json:"ref_count"`
}
