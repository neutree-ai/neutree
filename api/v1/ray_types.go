package v1

type Provider struct {
	Type               string   `json:"type,omitempty" yaml:"type,omitempty"`
	HeadIP             string   `json:"head_ip,omitempty" yaml:"head_ip,omitempty"`
	WorkerIPs          []string `json:"worker_ips,omitempty" yaml:"worker_ips"`
	CoordinatorAddress string   `json:"coordinator_address,omitempty" yaml:"coordinator_address,omitempty"`
	Region             string   `json:"region,omitempty" yaml:"region,omitempty"`
	AvailabilityZone   string   `json:"availability_zone,omitempty" yaml:"availability_zone,omitempty"`
	ProjectID          string   `json:"project_id,omitempty" yaml:"project_id,omitempty"`
}

type Auth struct {
	SSHUser       string `json:"ssh_user,omitempty" yaml:"ssh_user,omitempty"`
	SSHPrivateKey string `json:"ssh_private_key,omitempty" yaml:"ssh_private_key,omitempty"`
}

type Docker struct {
	Image            string   `json:"image,omitempty" yaml:"image,omitempty"`
	ContainerName    string   `json:"container_name,omitempty" yaml:"container_name,omitempty"`
	RunOptions       []string `json:"run_options,omitempty" yaml:"run_options,omitempty"`
	HeadRunOptions   []string `json:"head_run_options,omitempty" yaml:"head_run_options,omitempty"`
	WorkerRunOptions []string `json:"worker_run_options,omitempty" yaml:"worker_run_options,omitempty"`
	PullBeforeRun    bool     `json:"pull_before_run,omitempty" yaml:"pull_before_run,omitempty"`
}

type RayClusterConfig struct {
	ClusterName                  string   `json:"cluster_name,omitempty" yaml:"cluster_name,omitempty"`
	Provider                     Provider `json:"provider" yaml:"provider"`
	Auth                         Auth     `json:"auth" yaml:"auth"`
	Docker                       Docker   `json:"docker,omitempty" yaml:"docker,omitempty"`
	HeadStartRayCommands         []string `json:"head_start_ray_commands,omitempty" yaml:"head_start_ray_commands,omitempty"`
	WorkerStartRayCommands       []string `json:"worker_start_ray_commands,omitempty" yaml:"worker_start_ray_commands,omitempty"`
	StaticWorkerStartRayCommands []string `json:"-" yaml:"-"`
	HeadSetupCommands            []string `json:"head_setup_commands,omitempty" yaml:"head_setup_commands,omitempty"`
	WorkerSetupCommands          []string `json:"worker_setup_commands,omitempty" yaml:"worker_setup_commands,omitempty"`
	InitializationCommands       []string `json:"initialization_commands,omitempty" yaml:"initialization_commands,omitempty"`

	MaxWorkers         int     `json:"max_workers,omitempty" yaml:"max_workers,omitempty"`
	UpscalingSpeed     float64 `json:"upscaling_speed,omitempty" yaml:"upscaling_speed,omitempty"`
	IdleTimeoutMinutes int     `json:"idle_timeout_minutes,omitempty" yaml:"idle_timeout_minutes,omitempty"`
	AvailableNodeTypes any     `json:"available_node_types,omitempty" yaml:"available_node_types,omitempty"`
	HeadNodeType       string  `json:"head_node_type,omitempty" yaml:"head_node_type,omitempty"`
}

type RayClusterMetadataData struct {
	RayVersion    string `json:"rayVersion"`
	PythonVersion string `json:"pythonVersion"`
	SessionId     string `json:"sessionId"`
	GitCommit     string `json:"gitCommit"`
	OS            string `json:"os"`
}

type NodeSummary struct {
	IP       string `json:"ip"`
	Hostname string `json:"hostname"`
	Raylet   Raylet `json:"raylet"`
}

type Raylet struct {
	NodeID     string             `json:"nodeId"`
	Resources  map[string]float64 `json:"resourcesTotal"`
	State      string             `json:"state"`
	Labels     map[string]string  `json:"labels"`
	IsHeadNode bool               `json:"isHeadNode"`
}

type RayClusterAutoScaleStatus struct {
	AutoscalerReport AutoscalerReport `json:"autoscalerReport,omitempty"`
}

type NodeInfo struct {
	NodeIP        string `json:"node_ip"`
	NodeType      string `json:"node_type"`
	StatusMessage string `json:"status_message,omitempty"`
}

type AutoscalerReport struct {
	ActiveNodes     map[string]int    `json:"activeNodes,omitempty"`
	PendingNodes    []NodeInfo        `json:"pendingNodes,omitempty"`
	PendingLaunches map[string]int    `json:"pendingLaunches,omitempty"`
	FailedNodes     []NodeInfo        `json:"failedNodes,omitempty"`
	NodeTypeMapping map[string]string `json:"nodeTypeMapping,omitempty"`
	Legacy          bool              `json:"legacy,omitempty"`
}

type LocalNodeStatus struct {
	Tags  map[string]string `json:"tags,omitempty"`
	State string            `json:"state"`
}

type RayClusterStatus struct {
	RayVersion          string
	PythonVersion       string
	NeutreeServeVersion string
	ReadyNodes          int
	AutoScaleStatus     AutoScaleStatus
}

type AutoScaleStatus struct {
	ActiveNodes  int
	PendingNodes int
	FailedNodes  int
}
