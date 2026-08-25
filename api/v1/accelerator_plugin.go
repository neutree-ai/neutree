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
}

type AcceleratorProfile struct {
	// AcceleratorType identifies the accelerator plugin that produced this profile.
	AcceleratorType string `json:"accelerator_type"`
	// ClusterRuntime describes how cluster-level containers should access the accelerator.
	ClusterRuntime *RuntimeConfig `json:"cluster_runtime,omitempty"`
	// EngineRuntime describes how inference engine containers should access the accelerator.
	EngineRuntime *RuntimeConfig `json:"engine_runtime,omitempty"`
	// NodeAgentRuntime describes vendor-specific access required only by the
	// NodeAgent. It is intentionally independent from MetricsExporter.Runtime
	// so renderers cannot inherit exporter privilege or mounts implicitly.
	NodeAgentRuntime *NodeAgentRuntimeProfile `json:"node_agent_runtime,omitempty"`
	// VirtualizationMonitor identifies the vendor-managed monitor whose raw
	// metrics can be collected for accelerator virtualization. Renderers and
	// generic collectors consume this declaration without branching on vendor
	// names, selectors, or ports.
	VirtualizationMonitor *VirtualizationMonitorProfile `json:"virtualization_monitor,omitempty"`
	// MetricsExporter describes the optional metrics exporter used for accelerator
	// observability on Kubernetes and StaticNode clusters.
	MetricsExporter *AcceleratorExporterProfile `json:"metrics_exporter,omitempty"`
}

// VirtualizationMonitorProfile declares a Kubernetes-local monitor endpoint.
// The selected accelerator Profile owns the monitor identity because HAMi
// implementations use different device-plugin selectors and ports.
type VirtualizationMonitorProfile struct {
	// Namespace scopes Kubernetes Pod discovery. Empty preserves the backend
	// default namespace behavior.
	Namespace string `json:"namespace,omitempty"`
	// PodSelector identifies monitor Pods on a node.
	PodSelector map[string]string `json:"pod_selector,omitempty"`
	// Port is the monitor metrics port.
	Port int `json:"port,omitempty"`
	// MetricsPath is the monitor HTTP path, defaulting to /metrics when empty.
	MetricsPath string `json:"metrics_path,omitempty"`
}

// VirtualizationMonitorProfileEnvKey is the reserved NodeAgent environment
// variable carrying the selected Profile's complete monitor configuration.
// It remains one JSON document so NodeAgent and vmagent cannot independently
// lower individual selector, namespace, port, or path fields.
const VirtualizationMonitorProfileEnvKey = "NEUTREE_VIRTUALIZATION_MONITOR_PROFILE"

// NodeAgentRuntimeProfile declares the backend-specific NodeAgent access
// contract for an explicit accelerator profile.
type NodeAgentRuntimeProfile struct {
	// Privileged requests privileged execution on backends that support it.
	Privileged bool `json:"privileged,omitempty"`
	// Env declares NodeAgent-only environment variables. Exporter environment
	// variables are not implicitly inherited by the NodeAgent.
	Env map[string]string `json:"env,omitempty"`
	// Capabilities declares Linux capabilities required by the NodeAgent only.
	Capabilities *corev1.Capabilities `json:"capabilities,omitempty"`
	// Volumes declares structured host volumes required by the NodeAgent runtime.
	Volumes []ComponentVolume `json:"volumes,omitempty"`
	// VolumeMounts declares the matching container mounts for Volumes.
	VolumeMounts []ComponentVolumeMount `json:"volume_mounts,omitempty"`
	// Runtime selects the Docker runtime handler on StaticNode.
	Runtime string `json:"runtime,omitempty"`
	// DockerRunOptions are StaticNode-only Docker options; Kubernetes does not parse them.
	DockerRunOptions []string `json:"docker_run_options,omitempty"`
}

type AcceleratorExporterProfile struct {
	// Name is the exporter identity used for stable workload, container, and scrape-job names.
	Name string `json:"name,omitempty"`
	// Image is the exporter container image.
	Image string `json:"image,omitempty"`
	// Command overrides the exporter image entrypoint.
	Command []string `json:"command,omitempty"`
	// Args are passed to the exporter command or image entrypoint.
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
	Capabilities *corev1.Capabilities `json:"capabilities,omitempty"`
	// Privileged requests privileged execution on backends that support it.
	Privileged bool `json:"privileged,omitempty"`
	// NodeSelector is Kubernetes-only placement; StaticNode ignores it.
	NodeSelector map[string]string `json:"node_selector,omitempty"`
	// Volumes declares structured host volumes required by the exporter runtime.
	Volumes []ComponentVolume `json:"volumes,omitempty"`
	// VolumeMounts declares the matching container mounts for Volumes.
	VolumeMounts []ComponentVolumeMount `json:"volume_mounts,omitempty"`
	// Runtime selects the Docker runtime handler (e.g. "nvidia") on StaticNode.
	// Kubernetes does not parse it. Without this, exporter/node-agent
	// containers only received `--gpus all` and no `--runtime=`, which left GPU
	// injection to the default Docker device-request hook instead of the
	// nvidia runtime. Selecting the handler explicitly keeps device access in
	// the runtime shim's OCI rewrite (systemd-rebuildable) on modern toolkits.
	// omitempty is deliberate: unlike RuntimeConfig.Runtime (which is always
	// set for accelerator runtimes), an empty handler here is meaningful —
	// it means the exporter does not select a special runtime.
	Runtime string `json:"runtime,omitempty"`
	// DockerRunOptions is StaticNode-only Docker fallback; Kubernetes must not parse it.
	DockerRunOptions []string `json:"docker_run_options,omitempty"`
}

// ComponentHostPathType is the supported type of a structured host path volume.
type ComponentHostPathType string

const (
	ComponentHostPathTypeDirectory ComponentHostPathType = "directory"
	ComponentHostPathTypeSocket    ComponentHostPathType = "socket"
	ComponentHostPathTypeFile      ComponentHostPathType = "file"
)

// ComponentVolume declares a backend-neutral component volume.
type ComponentVolume struct {
	Name     string                         `json:"name,omitempty"`
	HostPath *ComponentHostPathVolumeSource `json:"host_path,omitempty"`
}

// ComponentHostPathVolumeSource describes a host path exposed to a component.
type ComponentHostPathVolumeSource struct {
	Path string                `json:"path,omitempty"`
	Type ComponentHostPathType `json:"type,omitempty"`
}

// ComponentVolumeMount declares where a component volume is mounted in a container.
type ComponentVolumeMount struct {
	Name      string `json:"name,omitempty"`
	MountPath string `json:"mount_path,omitempty"`
	ReadOnly  *bool  `json:"read_only,omitempty"`
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
