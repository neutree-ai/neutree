package v1

import (
	"strconv"

	"github.com/neutree-ai/neutree/pkg/scheme"
)

const (
	StaticNodeClusterKind = "StaticNodeCluster"
	StaticNodeKind        = "StaticNode"
)

type StaticNodeCluster struct {
	ID         int                      `json:"id,omitempty"`
	APIVersion string                   `json:"api_version,omitempty"`
	Kind       string                   `json:"kind,omitempty"`
	Metadata   *Metadata                `json:"metadata,omitempty"`
	Spec       *StaticNodeClusterSpec   `json:"spec,omitempty"`
	Status     *StaticNodeClusterStatus `json:"status,omitempty"`
}

type StaticNodeClusterSpec struct {
	// Version is the desired cluster runtime version used to build node components.
	Version string `json:"version,omitempty"`
	// ImageRegistry is the registry prefix used when rendering static node component images.
	ImageRegistry string `json:"image_registry,omitempty"`
	// Metrics configures static-node observability collectors.
	Metrics *ClusterMetricsConfig `json:"metrics,omitempty" yaml:"metrics,omitempty"`
	// Nodes declares the desired static machines that make up the cluster.
	Nodes []StaticNodeClusterNodeSpec `json:"nodes,omitempty"`
	// UpgradeStrategy controls how the static cluster rolls from the observed version to Version.
	UpgradeStrategy *ClusterUpgradeStrategy `json:"upgrade_strategy,omitempty"`
}

type StaticNodeClusterNodeSpec struct {
	// Name is the stable static node name within the cluster.
	Name string `json:"name,omitempty"`
	// IP is the SSH and Ray node address for this static machine.
	IP string `json:"ip,omitempty"`
	// Role defines whether this machine is the Ray head or a worker.
	Role StaticNodeRole `json:"role,omitempty"`
	// SSHAuth is the controller-only credential used to connect to the machine.
	SSHAuth *Auth `json:"ssh_auth,omitempty" api:"-"`
}

type StaticNodeClusterStatus struct {
	// Phase is the aggregated lifecycle phase of all static nodes in the cluster.
	Phase StaticNodeClusterPhase `json:"phase,omitempty"`
	// DesiredNodes is the number of nodes expected from the current cluster spec.
	DesiredNodes int `json:"desired_nodes,omitempty"`
	// ReadyNodes is the number of desired nodes currently reporting Ready.
	ReadyNodes int `json:"ready_nodes,omitempty"`
	// HeadReady reports whether the desired head node is Ready.
	HeadReady bool `json:"head_ready,omitempty"`
	// WarmReady reports whether all required image warm-up tasks are complete.
	WarmReady bool `json:"warm_ready,omitempty"`
	// Version is the observed cluster version once the desired runtime is ready.
	Version string `json:"version,omitempty"`
	// LastTransitionTime records when this status last changed phase.
	LastTransitionTime string `json:"last_transition_time,omitempty"`
	// ErrorMessage summarizes the blocking error or degraded reason.
	ErrorMessage string `json:"error_message,omitempty"`
}

type StaticNodeClusterPhase string

const (
	// StaticNodeClusterPhaseProvisioning means the desired static nodes or their desired component
	// observations have not fully converged yet.
	StaticNodeClusterPhaseProvisioning StaticNodeClusterPhase = "Provisioning"
	// StaticNodeClusterPhaseUpgrading means a recreate-style version upgrade is in progress.
	StaticNodeClusterPhaseUpgrading StaticNodeClusterPhase = "Upgrading"
	// StaticNodeClusterPhaseReady means all desired static nodes are ready, warm-up is complete,
	// the head node is ready, and desired components have been observed.
	StaticNodeClusterPhaseReady StaticNodeClusterPhase = "Ready"
	// StaticNodeClusterPhaseFailed means a desired node failed or the cluster spec is invalid.
	StaticNodeClusterPhaseFailed StaticNodeClusterPhase = "Failed"
)

type StaticNode struct {
	ID         int               `json:"id,omitempty"`
	APIVersion string            `json:"api_version,omitempty"`
	Kind       string            `json:"kind,omitempty"`
	Metadata   *Metadata         `json:"metadata,omitempty"`
	Spec       *StaticNodeSpec   `json:"spec,omitempty"`
	Status     *StaticNodeStatus `json:"status,omitempty"`
}

type StaticNodeSpec struct {
	// Cluster is the owning StaticNodeCluster name.
	Cluster string `json:"cluster,omitempty"`
	// IP is the SSH and runtime address of this static machine.
	IP string `json:"ip,omitempty"`
	// Role defines whether this node should run head or worker components.
	Role StaticNodeRole `json:"role,omitempty"`
	// SSHAuth is the controller-only credential used to reconcile this machine.
	SSHAuth *Auth `json:"ssh_auth,omitempty" api:"-"`
	// Warm describes images that should be present before component startup.
	Warm *WarmSpec `json:"warm,omitempty"`
	// Components is the desired set of containers and config files on this node.
	Components []NodeComponentSpec `json:"components,omitempty"`
}

type StaticNodeRole string

const (
	StaticNodeRoleHead   StaticNodeRole = "head"
	StaticNodeRoleWorker StaticNodeRole = "worker"
)

type StaticNodeStatus struct {
	// Phase is the node-level lifecycle phase observed by the static node controller.
	Phase StaticNodePhase `json:"phase,omitempty"`
	// Accelerator records the detected accelerator type and device inventory.
	Accelerator *StaticNodeAcceleratorStatus `json:"accelerator,omitempty"`
	// Allocations records workload-to-device assignments observed by the node agent.
	Allocations []StaticNodeAllocationStatus `json:"allocations,omitempty"`
	// Warm records image warm-up progress for this node.
	Warm *WarmStatus `json:"warm,omitempty"`
	// Components records observed state for each desired or stale component.
	Components []NodeComponentStatus `json:"components,omitempty"`
	// LastTransitionTime records when this status last changed phase.
	LastTransitionTime string `json:"last_transition_time,omitempty"`
	// ErrorMessage summarizes the blocking node-level error.
	ErrorMessage string `json:"error_message,omitempty"`
}

const StaticNodeAcceleratorTypeCPU = "cpu"

type StaticNodeAcceleratorStatus struct {
	// Type is the accelerator class selected by discovery, such as cpu or nvidia_gpu.
	Type string `json:"type,omitempty"`
	// Devices lists detected physical accelerator devices on the node.
	Devices []StaticNodeAcceleratorDeviceStatus `json:"devices,omitempty"`
}

const StaticNodeAcceleratorDeviceMinorNumberUnknown = -1

type StaticNodeAcceleratorDeviceStatus struct {
	// ID is the plugin-provided local device identifier.
	ID string `json:"id,omitempty"`
	// UUID is the stable device identifier used to correlate resource views.
	UUID string `json:"uuid,omitempty"`
	// ProductName is the device product name reported by the plugin.
	ProductName string `json:"product_name,omitempty"`
	// ProductModel is the normalized model key used for resource grouping when available.
	ProductModel string `json:"product_model,omitempty"`
	// MinorNumber is the Linux device minor number, for example the X in /dev/nvidiaX.
	// StaticNodeAcceleratorDeviceMinorNumberUnknown means the minor number is unknown.
	MinorNumber int `json:"minor_number,omitempty"`
	// MemoryMiB is the device memory capacity in MiB.
	MemoryMiB int64 `json:"memory_mib,omitempty"`
	// Healthy reports whether the plugin considers this device usable.
	Healthy bool `json:"healthy,omitempty"`
}

type StaticNodeAllocationStatus struct {
	WorkloadType string             `json:"workload_type,omitempty"`
	Workspace    string             `json:"workspace,omitempty"`
	Endpoint     string             `json:"endpoint,omitempty"`
	InstanceID   string             `json:"instance_id,omitempty"`
	ReplicaID    string             `json:"replica_id,omitempty"`
	RuntimeID    string             `json:"runtime_id,omitempty"`
	PID          int                `json:"pid,omitempty"`
	Devices      []DeviceAllocation `json:"devices,omitempty"`
}

func CPUStaticNodeAcceleratorStatus() StaticNodeAcceleratorStatus {
	return StaticNodeAcceleratorStatus{
		Type: StaticNodeAcceleratorTypeCPU,
	}
}

type StaticNodePhase string

const (
	// StaticNodePhasePending is reserved for discovery-safe nodes that have not produced enough
	// observed state for reconciliation to start.
	StaticNodePhasePending StaticNodePhase = "Pending"
	// StaticNodePhaseWarming means image warm-up has started but not all required images are ready.
	StaticNodePhaseWarming StaticNodePhase = "Warming"
	// StaticNodePhaseReconciling means warm-up is complete or not required, and component
	// reconciliation is still empty, pending, or not fully running.
	StaticNodePhaseReconciling StaticNodePhase = "Reconciling"
	// StaticNodePhaseReady means every desired component is running and ready.
	StaticNodePhaseReady StaticNodePhase = "Ready"
	// StaticNodePhaseFailed means node reconciliation failed and ErrorMessage records the cause.
	StaticNodePhaseFailed StaticNodePhase = "Failed"
)

type NodeComponentSpec struct {
	// Name is the stable component identity on the node.
	Name string `json:"name,omitempty"`
	// Image is the container image to run for this component.
	Image string `json:"image,omitempty"`
	// Command overrides the container entrypoint.
	Command []string `json:"command,omitempty"`
	// Args are passed to the container command or image entrypoint.
	Args []string `json:"args,omitempty"`
	// Env is the environment variable map passed to the container.
	Env map[string]string `json:"env,omitempty"`
	// Ports declares host ports expected to be exposed by the component.
	Ports []NodeComponentPort `json:"ports,omitempty"`
	// Volumes declares host path mounts for the component container.
	Volumes []NodeComponentVolume `json:"volumes,omitempty"`
	// DockerRunOptions are extra docker run flags appended by the controller.
	DockerRunOptions []string `json:"docker_run_options,omitempty"`
	// ConfigFiles declares files that must be written before starting the component.
	ConfigFiles []NodeComponentConfigFile `json:"config_files,omitempty"`
	// HealthCheck defines how the controller verifies the component after start.
	HealthCheck *NodeComponentHealthCheck `json:"health_check,omitempty"`
	// ConfigHash is the desired configuration fingerprint used to detect drift.
	ConfigHash string `json:"config_hash,omitempty"`
}

type NodeComponentPort struct {
	// Name is the logical port name.
	Name string `json:"name,omitempty"`
	// Port is the host port expected to be reachable.
	Port int `json:"port,omitempty"`
	// Protocol is the port protocol, for example TCP.
	Protocol string `json:"protocol,omitempty"`
}

type NodeComponentVolume struct {
	// Name is the logical mount name.
	Name string `json:"name,omitempty"`
	// HostPath is the path on the static node host.
	HostPath string `json:"host_path,omitempty"`
	// MountPath is the path inside the component container.
	MountPath string `json:"mount_path,omitempty"`
	// ReadOnly mounts the host path read-only when true.
	ReadOnly bool `json:"read_only,omitempty"`
}

type NodeComponentConfigFile struct {
	// Path is the target file path on the static node host.
	Path string `json:"path,omitempty"`
	// Content is the desired file content.
	Content string `json:"content,omitempty"`
	// Mode is the file permission mode passed to install, for example 0644.
	Mode string `json:"mode,omitempty"`
	// Owner is the desired file owner.
	Owner string `json:"owner,omitempty"`
	// Group is the desired file group.
	Group string `json:"group,omitempty"`
	// Sudo writes or removes the file through sudo when true.
	Sudo bool `json:"sudo,omitempty"`
	// Atomic stages and renames the file into place when true.
	Atomic bool `json:"atomic,omitempty"`
	// CreateParent creates the parent directory before writing when true.
	CreateParent bool `json:"create_parent,omitempty"`
	// SkipRestartOnChange excludes dynamic file contents from component hash.
	SkipRestartOnChange bool `json:"skip_restart_on_change,omitempty"`
}

type NodeComponentHealthCheck struct {
	// Command is an optional shell command used as the health probe.
	Command []string `json:"command,omitempty"`
	// HTTPHost overrides the host used for HTTP health checks.
	HTTPHost string `json:"http_host,omitempty"`
	// HTTPPath is the HTTP path used for health checks.
	HTTPPath string `json:"http_path,omitempty"`
	// Port is the HTTP health check port.
	Port int `json:"port,omitempty"`
	// InitialDelaySec is the intended delay before the first health check.
	InitialDelaySec int `json:"initial_delay_sec,omitempty"`
	// IntervalSec is the intended interval between health checks.
	IntervalSec int `json:"interval_sec,omitempty"`
	// TimeoutSec is the health check request timeout in seconds.
	TimeoutSec int `json:"timeout_sec,omitempty"`
}

type NodeComponentStatus struct {
	// Name is the component identity this status describes.
	Name string `json:"name,omitempty"`
	// Ready reports whether the component passed reconciliation and health checks.
	Ready bool `json:"ready,omitempty"`
	// Phase is the observed component lifecycle phase.
	Phase NodeComponentPhase `json:"phase,omitempty"`
	// ObservedHash is the configuration fingerprint observed during reconciliation.
	ObservedHash string `json:"observed_hash,omitempty"`
	// ObservedImage is the image observed or attempted during reconciliation.
	ObservedImage string `json:"observed_image,omitempty"`
	// Reason is a short machine-readable reason for the current phase.
	Reason string `json:"reason,omitempty"`
	// Message is a human-readable detail for the current phase.
	Message string `json:"message,omitempty"`
	// LastTransitionTime records when this component status last changed phase.
	LastTransitionTime string `json:"last_transition_time,omitempty"`
}

type NodeComponentPhase string

const (
	// NodeComponentPhasePending means the component is intentionally waiting for a prerequisite,
	// such as a worker component waiting for the head static node to become ready.
	NodeComponentPhasePending NodeComponentPhase = "Pending"
	// NodeComponentPhaseStarting means the controller could not confirm an existing matching
	// container and is starting or restarting it.
	NodeComponentPhaseStarting NodeComponentPhase = "Starting"
	// NodeComponentPhaseRunning means the component container matches the desired image/hash and
	// passed its configured health check.
	NodeComponentPhaseRunning NodeComponentPhase = "Running"
	// NodeComponentPhaseFailed means component reconciliation failed, such as image, config, or
	// container startup failure.
	NodeComponentPhaseFailed NodeComponentPhase = "Failed"
	// NodeComponentPhaseStopped means a previously observed stale component was removed.
	NodeComponentPhaseStopped NodeComponentPhase = "Stopped"
)

type WarmSpec struct {
	// Images is the desired image set that should exist locally before component startup.
	Images []WarmImageSpec `json:"images,omitempty"`
}

type WarmImageSpec struct {
	// Name is the logical warm-up item name used in status reporting.
	Name string `json:"name,omitempty"`
	// Ref is the container image reference to inspect or pull.
	Ref string `json:"ref,omitempty"`
	// Required makes warm-up failure block node readiness when true.
	Required bool `json:"required,omitempty"`
}

type WarmStatus struct {
	// Ready reports whether all required warm-up items are ready.
	Ready bool `json:"ready,omitempty"`
	// Images records per-image warm-up status.
	Images []WarmImageStatus `json:"images,omitempty"`
	// Reason is a short machine-readable reason for warm-up failure.
	Reason string `json:"reason,omitempty"`
	// Message is a human-readable warm-up failure detail.
	Message string `json:"message,omitempty"`
	// LastTransitionTime records when warm status last changed phase.
	LastTransitionTime string `json:"last_transition_time,omitempty"`
}

type WarmImageStatus struct {
	// Name is the logical warm-up item name from WarmImageSpec.
	Name string `json:"name,omitempty"`
	// Ref is the image reference inspected or pulled.
	Ref string `json:"ref,omitempty"`
	// Ready reports whether the image exists locally and has a digest.
	Ready bool `json:"ready,omitempty"`
	// Digest is the local image repo digest observed after inspect or pull.
	Digest string `json:"digest,omitempty"`
	// Phase is the image warm-up lifecycle phase.
	Phase WarmPhase `json:"phase,omitempty"`
	// Reason is a short machine-readable reason for the current phase.
	Reason string `json:"reason,omitempty"`
	// Message is a human-readable detail for the current phase.
	Message string `json:"message,omitempty"`
	// LastTransitionTime records when this image status last changed phase.
	LastTransitionTime string `json:"last_transition_time,omitempty"`
}

type WarmPhase string

const (
	WarmPhasePending WarmPhase = "Pending"
	WarmPhasePulling WarmPhase = "Pulling"
	WarmPhaseReady   WarmPhase = "Ready"
	WarmPhaseFailed  WarmPhase = "Failed"
)

type StaticCondition struct {
	Type               string `json:"type,omitempty"`
	Status             string `json:"status,omitempty"`
	Reason             string `json:"reason,omitempty"`
	Message            string `json:"message,omitempty"`
	LastTransitionTime string `json:"last_transition_time,omitempty"`
}

func (obj *StaticNodeCluster) Key() string {
	if obj.Metadata == nil {
		return "default-staticnodecluster-" + strconv.Itoa(obj.ID)
	}

	if obj.Metadata.Workspace == "" {
		return "default-staticnodecluster-" + strconv.Itoa(obj.ID) + "-" + obj.Metadata.Name
	}

	return obj.Metadata.Workspace + "-staticnodecluster-" + strconv.Itoa(obj.ID) + "-" + obj.Metadata.Name
}

func (obj *StaticNodeCluster) GetName() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Name
}

func (obj *StaticNodeCluster) GetWorkspace() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Workspace
}

func (obj *StaticNodeCluster) GetLabels() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Labels
}

func (obj *StaticNodeCluster) SetLabels(labels map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Labels = labels
}

func (obj *StaticNodeCluster) GetAnnotations() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Annotations
}

func (obj *StaticNodeCluster) SetAnnotations(annotations map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Annotations = annotations
}

func (obj *StaticNodeCluster) GetCreationTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.CreationTimestamp
}

func (obj *StaticNodeCluster) GetUpdateTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.UpdateTimestamp
}

func (obj *StaticNodeCluster) GetDeletionTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.DeletionTimestamp
}

func (obj *StaticNodeCluster) GetSpec() interface{} {
	return obj.Spec
}

func (obj *StaticNodeCluster) GetStatus() interface{} {
	return obj.Status
}

func (obj *StaticNodeCluster) GetKind() string {
	return obj.Kind
}

func (obj *StaticNodeCluster) SetKind(kind string) {
	obj.Kind = kind
}

func (obj *StaticNodeCluster) GetID() string {
	return strconv.Itoa(obj.ID)
}

func (obj *StaticNodeCluster) GetMetadata() interface{} {
	return obj.Metadata
}

func (obj *StaticNode) Key() string {
	if obj.Metadata == nil {
		return "default-staticnode-" + strconv.Itoa(obj.ID)
	}

	if obj.Metadata.Workspace == "" {
		return "default-staticnode-" + strconv.Itoa(obj.ID) + "-" + obj.Metadata.Name
	}

	return obj.Metadata.Workspace + "-staticnode-" + strconv.Itoa(obj.ID) + "-" + obj.Metadata.Name
}

func (obj *StaticNode) GetName() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Name
}

func (obj *StaticNode) GetWorkspace() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Workspace
}

func (obj *StaticNode) GetLabels() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Labels
}

func (obj *StaticNode) SetLabels(labels map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Labels = labels
}

func (obj *StaticNode) GetAnnotations() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Annotations
}

func (obj *StaticNode) SetAnnotations(annotations map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Annotations = annotations
}

func (obj *StaticNode) GetCreationTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.CreationTimestamp
}

func (obj *StaticNode) GetUpdateTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.UpdateTimestamp
}

func (obj *StaticNode) GetDeletionTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.DeletionTimestamp
}

func (obj *StaticNode) GetSpec() interface{} {
	return obj.Spec
}

func (obj *StaticNode) GetStatus() interface{} {
	return obj.Status
}

func (obj *StaticNode) GetKind() string {
	return obj.Kind
}

func (obj *StaticNode) SetKind(kind string) {
	obj.Kind = kind
}

func (obj *StaticNode) GetID() string {
	return strconv.Itoa(obj.ID)
}

func (obj *StaticNode) GetMetadata() interface{} {
	return obj.Metadata
}

type StaticNodeClusterList struct {
	Kind  string              `json:"kind"`
	Items []StaticNodeCluster `json:"items"`
}

func (in *StaticNodeClusterList) GetKind() string {
	return in.Kind
}

func (in *StaticNodeClusterList) SetKind(kind string) {
	in.Kind = kind
}

func (in *StaticNodeClusterList) GetItems() []scheme.Object {
	objs := make([]scheme.Object, 0, len(in.Items))
	for i := range in.Items {
		objs = append(objs, &in.Items[i])
	}

	return objs
}

func (in *StaticNodeClusterList) SetItems(objs []scheme.Object) {
	items := make([]StaticNodeCluster, len(objs))
	for i, obj := range objs {
		items[i] = *obj.(*StaticNodeCluster) //nolint:errcheck
	}

	in.Items = items
}

type StaticNodeList struct {
	Kind  string       `json:"kind"`
	Items []StaticNode `json:"items"`
}

func (in *StaticNodeList) GetKind() string {
	return in.Kind
}

func (in *StaticNodeList) SetKind(kind string) {
	in.Kind = kind
}

func (in *StaticNodeList) GetItems() []scheme.Object {
	objs := make([]scheme.Object, 0, len(in.Items))
	for i := range in.Items {
		objs = append(objs, &in.Items[i])
	}

	return objs
}

func (in *StaticNodeList) SetItems(objs []scheme.Object) {
	items := make([]StaticNode, len(objs))
	for i, obj := range objs {
		items[i] = *obj.(*StaticNode) //nolint:errcheck
	}

	in.Items = items
}
