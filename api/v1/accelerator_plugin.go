package v1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

type AcceleratorType string

func (at AcceleratorType) String() string {
	return string(at)
}

func (at AcceleratorType) StringPtr() *string {
	s := at.String()
	return &s
}

// Accelerator type constants
const (
	AcceleratorTypeNVIDIAGPU AcceleratorType = "nvidia_gpu"
	AcceleratorTypeAMDGPU    AcceleratorType = "amd_gpu"
)

type AcceleratorProduct string

type Accelerator struct {
	ID   string `json:"id,omitempty"`
	Type string `json:"type,omitempty"`
}

type GetNodeAcceleratorRequest struct {
	NodeIp  string `json:"node_ip"`
	SSHAuth Auth   `json:"ssh_auth"`
}

type GetNodeAcceleratorResponse struct {
	Accelerators []Accelerator `json:"accelerators"`
}

type GetNodeRuntimeConfigRequest struct {
	NodeIp  string `json:"node_ip"`
	SSHAuth Auth   `json:"ssh_auth"`
}

type GetNodeRuntimeConfigResponse struct {
	RuntimeConfig RuntimeConfig `json:"runtime_config"`
}

type GetContainerAcceleratorRequest struct {
	Container corev1.Container `json:"container"`
}

type GetContainerAcceleratorResponse struct {
	Accelerators []Accelerator `json:"accelerators"`
}

type GetContainerRuntimeConfigRequest struct {
	Container corev1.Container `json:"container"`
}

type GetContainerRuntimeConfigResponse struct {
	RuntimeConfig RuntimeConfig `json:"runtime_config"`
}

type GetAcceleratorProfileResponse struct {
	Profile AcceleratorProfile `json:"profile"`
}

type DetectStaticNodeAcceleratorRequest struct {
	NodeIp  string `json:"node_ip"`
	SSHAuth Auth   `json:"ssh_auth"`
}

type DetectStaticNodeAcceleratorResponse struct {
	Accelerator *StaticNodeAcceleratorStatus `json:"accelerator,omitempty"`
	Matched     bool                         `json:"matched"`
}

type RuntimeConfig struct {
	ImageSuffix string            `json:"image_suffix"`
	Env         map[string]string `json:"env"`
	Runtime     string            `json:"runtime"`
	Options     []string          `json:"options"`
	// CDIDevices declares the CDI device requests for containers that need
	// accelerator access on Docker/static-node backends. It renders as
	// `--device <cdi>` and replaces legacy bare `--gpus all` injection. Empty
	// means the component does not need accelerator devices (CPU / non-GPU).
	// The Runtime field only selects the handler; devices are declared here.
	CDIDevices []string `json:"cdi_devices,omitempty"`
}

type AcceleratorProfile struct {
	// AcceleratorType identifies the accelerator plugin that produced this profile.
	AcceleratorType string `json:"accelerator_type"`
	// ClusterRuntime describes how cluster-level containers should access the accelerator.
	ClusterRuntime *RuntimeConfig `json:"cluster_runtime,omitempty"`
	// EngineRuntime describes how inference engine containers should access the accelerator.
	EngineRuntime *RuntimeConfig `json:"engine_runtime,omitempty"`
	// MetricsExporter describes the optional metrics exporter used for accelerator observability.
	MetricsExporter *AcceleratorExporterProfile `json:"metrics_exporter,omitempty"`
}

type AcceleratorExporterProfile struct {
	// Name is the exporter identity used for stable workload, container, and scrape-job names.
	Name string `json:"name,omitempty"`
	// Image is the exporter container image.
	Image string `json:"image,omitempty"`
	// Args are passed to the exporter image entrypoint.
	Args []string `json:"args,omitempty"`
	// Port is the metrics port exposed by the exporter.
	Port int `json:"port,omitempty"`
	// MetricsPath is the HTTP path scraped by vmagent; it defaults to /metrics when empty.
	MetricsPath string `json:"metrics_path,omitempty"`
	// Env contains exporter environment variables.
	Env map[string]string `json:"env,omitempty"`
	// ConfigFiles declares exporter configuration files that must be materialized before start.
	ConfigFiles []AcceleratorExporterConfigFile `json:"config_files,omitempty"`
	// Runtime declares backend-specific runtime requirements for running the exporter.
	Runtime *AcceleratorExporterRuntimeProfile `json:"runtime,omitempty"`
}

type AcceleratorExporterConfigFile struct {
	// Path is the file path consumed by the exporter.
	Path string `json:"path,omitempty"`
	// Content is the desired file content.
	Content string `json:"content,omitempty"`
	// Mode is the file permission mode used by backends that materialize host files.
	Mode string `json:"mode,omitempty"`
	// Owner is the desired file owner used by backends that materialize host files.
	Owner string `json:"owner,omitempty"`
	// Group is the desired file group used by backends that materialize host files.
	Group string `json:"group,omitempty"`
	// Sudo writes the file through elevated privileges on backends that need it.
	Sudo bool `json:"sudo,omitempty"`
	// Atomic stages and renames the file into place on backends that support atomic writes.
	Atomic bool `json:"atomic,omitempty"`
	// CreateParent creates the parent directory before writing on backends that materialize host files.
	CreateParent bool `json:"create_parent,omitempty"`
	// SkipRestartOnChange excludes dynamic file contents from restart decisions on backends that hash config.
	SkipRestartOnChange bool `json:"skip_restart_on_change,omitempty"`
}

type AcceleratorExporterRuntimeProfile struct {
	// HostNetwork is supported by StaticNode and Kubernetes when the backend has an equivalent.
	HostNetwork bool `json:"host_network,omitempty"`
	// HostPID is supported by StaticNode and Kubernetes when the backend has an equivalent.
	HostPID bool `json:"host_pid,omitempty"`
	// Capabilities is supported by StaticNode and Kubernetes when the backend has an equivalent.
	Capabilities *AcceleratorExporterCapabilities `json:"capabilities,omitempty"`
	// NodeSelector is Kubernetes-only placement; StaticNode ignores it.
	NodeSelector map[string]string `json:"node_selector,omitempty"`
	// Runtime selects the Docker runtime handler (e.g. "nvidia") on StaticNode.
	// Kubernetes does not parse it. Without this, exporter/node-agent
	// containers only received `--gpus all` and no `--runtime=`, which left the
	// GPU permission injection to the legacy nvidia-container-runtime-hook.
	Runtime string `json:"runtime,omitempty"`
	// CDIDevices declares the CDI device requests for the component on
	// StaticNode, rendered as docker `--device <cdi>`. It replaces bare
	// `--gpus all` in DockerRunOptions. Kubernetes must not parse it.
	CDIDevices []string `json:"cdi_devices,omitempty"`
	// DockerRunOptions is StaticNode-only Docker fallback; Kubernetes must not parse it.
	DockerRunOptions []string `json:"docker_run_options,omitempty"`
}

type AcceleratorExporterCapabilities struct {
	Add []string `json:"add,omitempty"`
}

type GetSupportEnginesResponse struct {
	Engines []*Engine `json:"engines"`
}

type RegisterEngineRequest struct {
	Engines []*Engine `json:"engines"`
}

type ParseFromKubernetesRequest struct {
	Resource map[corev1.ResourceName]resource.Quantity `json:"resource"`
	Labels   map[string]string                         `json:"labels"`
}

type ParseFromRayRequest struct {
	Resource map[string]float64 `json:"resource"`
}
