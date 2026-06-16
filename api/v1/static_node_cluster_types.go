package v1

import (
	"strconv"

	"github.com/neutree-ai/neutree/pkg/scheme"
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
	Version               string                      `json:"version,omitempty"`
	ImageRegistry         string                      `json:"image_registry,omitempty"`
	MetricsRemoteWriteURL string                      `json:"metrics_remote_write_url,omitempty"`
	Head                  StaticNodeClusterHeadSpec   `json:"head,omitempty"`
	Nodes                 []StaticNodeClusterNodeSpec `json:"nodes,omitempty"`
	UpgradeStrategy       *ClusterUpgradeStrategy     `json:"upgrade_strategy,omitempty"`
}

type StaticNodeClusterHeadSpec struct {
	NodeName string `json:"node_name,omitempty"`
}

type StaticNodeClusterNodeSpec struct {
	Name       string         `json:"name,omitempty"`
	IP         string         `json:"ip,omitempty"`
	Role       StaticNodeRole `json:"role,omitempty"`
	SSHAuthRef string         `json:"ssh_auth_ref,omitempty"`
	SSHAuth    *Auth          `json:"ssh_auth,omitempty" api:"-"`
}

type StaticNodeClusterStatus struct {
	Phase              StaticNodeClusterPhase          `json:"phase,omitempty"`
	DesiredNodes       int                             `json:"desired_nodes,omitempty"`
	ReadyNodes         int                             `json:"ready_nodes,omitempty"`
	HeadReady          bool                            `json:"head_ready,omitempty"`
	MetricsReady       bool                            `json:"metrics_ready,omitempty"`
	WarmReady          bool                            `json:"warm_ready,omitempty"`
	Upgrade            *StaticNodeClusterUpgradeStatus `json:"upgrade,omitempty"`
	LastTransitionTime string                          `json:"last_transition_time,omitempty"`
	ErrorMessage       string                          `json:"error_message,omitempty"`
}

type StaticNodeClusterUpgradeStatus struct {
	ObservedVersion string                       `json:"observed_version,omitempty"`
	TargetVersion   string                       `json:"target_version,omitempty"`
	Step            StaticNodeClusterUpgradeStep `json:"step,omitempty"`
}

type StaticNodeClusterUpgradeStep string

const (
	StaticNodeClusterUpgradeStepWarming         StaticNodeClusterUpgradeStep = "Warming"
	StaticNodeClusterUpgradeStepStoppingWorkers StaticNodeClusterUpgradeStep = "StoppingWorkers"
	StaticNodeClusterUpgradeStepStartingHead    StaticNodeClusterUpgradeStep = "StartingHead"
	StaticNodeClusterUpgradeStepStartingWorkers StaticNodeClusterUpgradeStep = "StartingWorkers"
	StaticNodeClusterUpgradeStepVerifying       StaticNodeClusterUpgradeStep = "Verifying"
)

type StaticNodeClusterPhase string

const (
	StaticNodeClusterPhaseProvisioning StaticNodeClusterPhase = "Provisioning"
	StaticNodeClusterPhaseWarming      StaticNodeClusterPhase = "Warming"
	StaticNodeClusterPhaseStopping     StaticNodeClusterPhase = "Stopping"
	StaticNodeClusterPhaseStarting     StaticNodeClusterPhase = "Starting"
	StaticNodeClusterPhaseVerifying    StaticNodeClusterPhase = "Verifying"
	StaticNodeClusterPhaseReady        StaticNodeClusterPhase = "Ready"
	StaticNodeClusterPhaseDegraded     StaticNodeClusterPhase = "Degraded"
	StaticNodeClusterPhaseFailed       StaticNodeClusterPhase = "Failed"
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
	Cluster    string              `json:"cluster,omitempty"`
	IP         string              `json:"ip,omitempty"`
	Role       StaticNodeRole      `json:"role,omitempty"`
	SSHAuthRef string              `json:"ssh_auth_ref,omitempty"`
	SSHAuth    *Auth               `json:"ssh_auth,omitempty" api:"-"`
	Warm       *WarmSpec           `json:"warm,omitempty"`
	Components []NodeComponentSpec `json:"components,omitempty"`
}

type StaticNodeRole string

const (
	StaticNodeRoleHead   StaticNodeRole = "head"
	StaticNodeRoleWorker StaticNodeRole = "worker"
)

type StaticNodeStatus struct {
	Phase              StaticNodePhase              `json:"phase,omitempty"`
	Accelerator        *StaticNodeAcceleratorStatus `json:"accelerator,omitempty"`
	Warm               *WarmStatus                  `json:"warm,omitempty"`
	Components         []NodeComponentStatus        `json:"components,omitempty"`
	LastTransitionTime string                       `json:"last_transition_time,omitempty"`
	ErrorMessage       string                       `json:"error_message,omitempty"`
}

const StaticNodeAcceleratorTypeCPU = "cpu"

type StaticNodeAcceleratorStatus struct {
	Type           string                              `json:"type,omitempty"`
	Vendor         string                              `json:"vendor,omitempty"`
	ProductName    string                              `json:"product_name,omitempty"`
	ProductModel   string                              `json:"product_model,omitempty"`
	RuntimeProfile string                              `json:"runtime_profile,omitempty"`
	ResourceName   string                              `json:"resource_name,omitempty"`
	Devices        []StaticNodeAcceleratorDeviceStatus `json:"devices,omitempty"`
}

type StaticNodeAcceleratorDeviceStatus struct {
	ID          string `json:"id,omitempty"`
	UUID        string `json:"uuid,omitempty"`
	ProductName string `json:"product_name,omitempty"`
	Healthy     bool   `json:"healthy,omitempty"`
}

func CPUStaticNodeAcceleratorStatus() StaticNodeAcceleratorStatus {
	return StaticNodeAcceleratorStatus{
		Type:           StaticNodeAcceleratorTypeCPU,
		Vendor:         "generic",
		ProductName:    "CPU",
		ProductModel:   StaticNodeAcceleratorTypeCPU,
		RuntimeProfile: StaticNodeAcceleratorTypeCPU,
		ResourceName:   "CPU",
		Devices:        []StaticNodeAcceleratorDeviceStatus{},
	}
}

type StaticNodePhase string

const (
	StaticNodePhasePending     StaticNodePhase = "Pending"
	StaticNodePhaseWarming     StaticNodePhase = "Warming"
	StaticNodePhaseReconciling StaticNodePhase = "Reconciling"
	StaticNodePhaseReady       StaticNodePhase = "Ready"
	StaticNodePhaseDegraded    StaticNodePhase = "Degraded"
	StaticNodePhaseFailed      StaticNodePhase = "Failed"
)

type NodeComponentSpec struct {
	Name             string                     `json:"name,omitempty"`
	Type             NodeComponentType          `json:"type,omitempty"`
	Image            string                     `json:"image,omitempty"`
	Command          []string                   `json:"command,omitempty"`
	Args             []string                   `json:"args,omitempty"`
	Env              map[string]string          `json:"env,omitempty"`
	Ports            []NodeComponentPort        `json:"ports,omitempty"`
	Volumes          []NodeComponentVolume      `json:"volumes,omitempty"`
	DockerRunOptions []string                   `json:"docker_run_options,omitempty"`
	ConfigFiles      []NodeComponentConfigFile  `json:"config_files,omitempty"`
	HealthCheck      *NodeComponentHealthCheck  `json:"health_check,omitempty"`
	Dependencies     []string                   `json:"dependencies,omitempty"`
	RestartPolicy    NodeComponentRestartPolicy `json:"restart_policy,omitempty"`
	DesiredPhase     NodeComponentPhase         `json:"desired_phase,omitempty"`
	ConfigHash       string                     `json:"config_hash,omitempty"`
}

type NodeComponentType string

const (
	NodeComponentTypeRayHead             NodeComponentType = "ray-head"
	NodeComponentTypeRayWorker           NodeComponentType = "ray-worker"
	NodeComponentTypeNodeExporter        NodeComponentType = "node-exporter"
	NodeComponentTypeAcceleratorExporter NodeComponentType = "accelerator-exporter"
	NodeComponentTypeMetricsAgent        NodeComponentType = "metrics-agent"
	NodeComponentTypeMetricsNormalizer   NodeComponentType = "metrics-normalizer"
)

type NodeComponentPort struct {
	Name     string `json:"name,omitempty"`
	Port     int    `json:"port,omitempty"`
	Protocol string `json:"protocol,omitempty"`
}

type NodeComponentVolume struct {
	Name      string `json:"name,omitempty"`
	HostPath  string `json:"host_path,omitempty"`
	MountPath string `json:"mount_path,omitempty"`
	ReadOnly  bool   `json:"read_only,omitempty"`
}

type NodeComponentConfigFile struct {
	Path         string `json:"path,omitempty"`
	Content      string `json:"content,omitempty"`
	Mode         string `json:"mode,omitempty"`
	Owner        string `json:"owner,omitempty"`
	Group        string `json:"group,omitempty"`
	Sudo         bool   `json:"sudo,omitempty"`
	Atomic       bool   `json:"atomic,omitempty"`
	CreateParent bool   `json:"create_parent,omitempty"`
	// SkipRestartOnChange excludes dynamic file contents from component hash.
	SkipRestartOnChange bool `json:"skip_restart_on_change,omitempty"`
}

type NodeComponentHealthCheck struct {
	Command         []string `json:"command,omitempty"`
	HTTPHost        string   `json:"http_host,omitempty"`
	HTTPPath        string   `json:"http_path,omitempty"`
	Port            int      `json:"port,omitempty"`
	InitialDelaySec int      `json:"initial_delay_sec,omitempty"`
	IntervalSec     int      `json:"interval_sec,omitempty"`
	TimeoutSec      int      `json:"timeout_sec,omitempty"`
}

type NodeComponentRestartPolicy string

const (
	NodeComponentRestartPolicyAlways    NodeComponentRestartPolicy = "Always"
	NodeComponentRestartPolicyOnFailure NodeComponentRestartPolicy = "OnFailure"
	NodeComponentRestartPolicyNever     NodeComponentRestartPolicy = "Never"
)

type NodeComponentStatus struct {
	Name               string             `json:"name,omitempty"`
	Type               NodeComponentType  `json:"type,omitempty"`
	Ready              bool               `json:"ready,omitempty"`
	Phase              NodeComponentPhase `json:"phase,omitempty"`
	ObservedHash       string             `json:"observed_hash,omitempty"`
	ObservedImage      string             `json:"observed_image,omitempty"`
	Reason             string             `json:"reason,omitempty"`
	Message            string             `json:"message,omitempty"`
	LastTransitionTime string             `json:"last_transition_time,omitempty"`
}

type NodeComponentPhase string

const (
	NodeComponentPhasePending  NodeComponentPhase = "Pending"
	NodeComponentPhaseStarting NodeComponentPhase = "Starting"
	NodeComponentPhaseRunning  NodeComponentPhase = "Running"
	NodeComponentPhaseDegraded NodeComponentPhase = "Degraded"
	NodeComponentPhaseFailed   NodeComponentPhase = "Failed"
	NodeComponentPhaseStopped  NodeComponentPhase = "Stopped"
)

type WarmSpec struct {
	Images []WarmImageSpec `json:"images,omitempty"`
}

type WarmImageSpec struct {
	Name     string `json:"name,omitempty"`
	Ref      string `json:"ref,omitempty"`
	Required bool   `json:"required,omitempty"`
}

type WarmStatus struct {
	Ready              bool              `json:"ready,omitempty"`
	Images             []WarmImageStatus `json:"images,omitempty"`
	Reason             string            `json:"reason,omitempty"`
	Message            string            `json:"message,omitempty"`
	LastTransitionTime string            `json:"last_transition_time,omitempty"`
}

type WarmImageStatus struct {
	Name               string    `json:"name,omitempty"`
	Ref                string    `json:"ref,omitempty"`
	Ready              bool      `json:"ready,omitempty"`
	Digest             string    `json:"digest,omitempty"`
	Phase              WarmPhase `json:"phase,omitempty"`
	Reason             string    `json:"reason,omitempty"`
	Message            string    `json:"message,omitempty"`
	LastTransitionTime string    `json:"last_transition_time,omitempty"`
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
