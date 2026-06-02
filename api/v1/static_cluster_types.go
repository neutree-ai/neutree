package v1

import (
	"strconv"

	"github.com/neutree-ai/neutree/pkg/scheme"
)

type StaticCluster struct {
	ID         int                  `json:"id,omitempty"`
	APIVersion string               `json:"api_version,omitempty"`
	Kind       string               `json:"kind,omitempty"`
	Metadata   *Metadata            `json:"metadata,omitempty"`
	Spec       *StaticClusterSpec   `json:"spec,omitempty"`
	Status     *StaticClusterStatus `json:"status,omitempty"`
}

type StaticClusterSpec struct {
	Version               string                        `json:"version,omitempty"`
	ImageRegistry         string                        `json:"image_registry,omitempty"`
	MetricsRemoteWriteURL string                        `json:"metrics_remote_write_url,omitempty"`
	Head                  StaticClusterHeadSpec         `json:"head,omitempty"`
	Nodes                 []StaticClusterNodeSpec       `json:"nodes,omitempty"`
	Warm                  *WarmSpec                     `json:"warm,omitempty"`
	UpgradeStrategy       *StaticClusterUpgradeStrategy `json:"upgrade_strategy,omitempty"`
}

type StaticClusterHeadSpec struct {
	NodeName string `json:"node_name,omitempty"`
}

type StaticClusterNodeSpec struct {
	Name            string         `json:"name,omitempty"`
	IP              string         `json:"ip,omitempty"`
	Role            StaticNodeRole `json:"role,omitempty"`
	AcceleratorType string         `json:"accelerator_type,omitempty"`
	SSHAuthRef      string         `json:"ssh_auth_ref,omitempty"`
}

type StaticClusterUpgradeStrategy struct {
	StopStart bool `json:"stop_start,omitempty"`
}

type StaticClusterStatus struct {
	Phase              StaticClusterPhase `json:"phase,omitempty"`
	DesiredNodes       int                `json:"desired_nodes,omitempty"`
	ReadyNodes         int                `json:"ready_nodes,omitempty"`
	HeadReady          bool               `json:"head_ready,omitempty"`
	MetricsReady       bool               `json:"metrics_ready,omitempty"`
	WarmReady          bool               `json:"warm_ready,omitempty"`
	LastTransitionTime string             `json:"last_transition_time,omitempty"`
	ErrorMessage       string             `json:"error_message,omitempty"`
	Conditions         []StaticCondition  `json:"conditions,omitempty"`
}

type StaticClusterPhase string

const (
	StaticClusterPhaseProvisioning StaticClusterPhase = "Provisioning"
	StaticClusterPhaseWarming      StaticClusterPhase = "Warming"
	StaticClusterPhaseStopping     StaticClusterPhase = "Stopping"
	StaticClusterPhaseStarting     StaticClusterPhase = "Starting"
	StaticClusterPhaseVerifying    StaticClusterPhase = "Verifying"
	StaticClusterPhaseReady        StaticClusterPhase = "Ready"
	StaticClusterPhaseDegraded     StaticClusterPhase = "Degraded"
	StaticClusterPhaseFailed       StaticClusterPhase = "Failed"
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
	Cluster         string           `json:"cluster,omitempty"`
	IP              string           `json:"ip,omitempty"`
	Role            StaticNodeRole   `json:"role,omitempty"`
	AcceleratorType string           `json:"accelerator_type,omitempty"`
	SSHAuthRef      string           `json:"ssh_auth_ref,omitempty"`
	Warm            *WarmSpec        `json:"warm,omitempty"`
	Workers         []NodeWorkerSpec `json:"workers,omitempty"`
}

type StaticNodeRole string

const (
	StaticNodeRoleHead   StaticNodeRole = "head"
	StaticNodeRoleWorker StaticNodeRole = "worker"
)

type StaticNodeStatus struct {
	Phase              StaticNodePhase    `json:"phase,omitempty"`
	Warm               *WarmStatus        `json:"warm,omitempty"`
	Workers            []NodeWorkerStatus `json:"workers,omitempty"`
	LastTransitionTime string             `json:"last_transition_time,omitempty"`
	ErrorMessage       string             `json:"error_message,omitempty"`
	Conditions         []StaticCondition  `json:"conditions,omitempty"`
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

type NodeWorkerSpec struct {
	Name             string                  `json:"name,omitempty"`
	Type             NodeWorkerType          `json:"type,omitempty"`
	Image            string                  `json:"image,omitempty"`
	Command          []string                `json:"command,omitempty"`
	Args             []string                `json:"args,omitempty"`
	Env              map[string]string       `json:"env,omitempty"`
	Ports            []NodeWorkerPort        `json:"ports,omitempty"`
	Volumes          []NodeWorkerVolume      `json:"volumes,omitempty"`
	DockerRunOptions []string                `json:"docker_run_options,omitempty"`
	ConfigFiles      []NodeWorkerConfigFile  `json:"config_files,omitempty"`
	HealthCheck      *NodeWorkerHealthCheck  `json:"health_check,omitempty"`
	Dependencies     []string                `json:"dependencies,omitempty"`
	RestartPolicy    NodeWorkerRestartPolicy `json:"restart_policy,omitempty"`
	ConfigHash       string                  `json:"config_hash,omitempty"`
}

type NodeWorkerType string

const (
	NodeWorkerTypeRayHead             NodeWorkerType = "ray-head"
	NodeWorkerTypeRayWorker           NodeWorkerType = "ray-worker"
	NodeWorkerTypeNodeExporter        NodeWorkerType = "node-exporter"
	NodeWorkerTypeAcceleratorExporter NodeWorkerType = "accelerator-exporter"
	NodeWorkerTypeMetricsAgent        NodeWorkerType = "metrics-agent"
	NodeWorkerTypeMetricsNormalizer   NodeWorkerType = "metrics-normalizer"
)

type NodeWorkerPort struct {
	Name     string `json:"name,omitempty"`
	Port     int    `json:"port,omitempty"`
	Protocol string `json:"protocol,omitempty"`
}

type NodeWorkerVolume struct {
	Name      string `json:"name,omitempty"`
	HostPath  string `json:"host_path,omitempty"`
	MountPath string `json:"mount_path,omitempty"`
	ReadOnly  bool   `json:"read_only,omitempty"`
}

type NodeWorkerConfigFile struct {
	Path         string `json:"path,omitempty"`
	Content      string `json:"content,omitempty"`
	Mode         string `json:"mode,omitempty"`
	Owner        string `json:"owner,omitempty"`
	Group        string `json:"group,omitempty"`
	Sudo         bool   `json:"sudo,omitempty"`
	Atomic       bool   `json:"atomic,omitempty"`
	CreateParent bool   `json:"create_parent,omitempty"`
}

type NodeWorkerHealthCheck struct {
	Command         []string `json:"command,omitempty"`
	HTTPPath        string   `json:"http_path,omitempty"`
	Port            int      `json:"port,omitempty"`
	InitialDelaySec int      `json:"initial_delay_sec,omitempty"`
	IntervalSec     int      `json:"interval_sec,omitempty"`
	TimeoutSec      int      `json:"timeout_sec,omitempty"`
}

type NodeWorkerRestartPolicy string

const (
	NodeWorkerRestartPolicyAlways    NodeWorkerRestartPolicy = "Always"
	NodeWorkerRestartPolicyOnFailure NodeWorkerRestartPolicy = "OnFailure"
	NodeWorkerRestartPolicyNever     NodeWorkerRestartPolicy = "Never"
)

type NodeWorkerStatus struct {
	Name               string          `json:"name,omitempty"`
	Type               NodeWorkerType  `json:"type,omitempty"`
	Ready              bool            `json:"ready,omitempty"`
	Phase              NodeWorkerPhase `json:"phase,omitempty"`
	ObservedHash       string          `json:"observed_hash,omitempty"`
	ObservedImage      string          `json:"observed_image,omitempty"`
	Reason             string          `json:"reason,omitempty"`
	Message            string          `json:"message,omitempty"`
	LastTransitionTime string          `json:"last_transition_time,omitempty"`
}

type NodeWorkerPhase string

const (
	NodeWorkerPhasePending  NodeWorkerPhase = "Pending"
	NodeWorkerPhaseStarting NodeWorkerPhase = "Starting"
	NodeWorkerPhaseRunning  NodeWorkerPhase = "Running"
	NodeWorkerPhaseDegraded NodeWorkerPhase = "Degraded"
	NodeWorkerPhaseFailed   NodeWorkerPhase = "Failed"
	NodeWorkerPhaseStopped  NodeWorkerPhase = "Stopped"
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

func (obj *StaticCluster) Key() string {
	if obj.Metadata == nil {
		return "default-staticcluster-" + strconv.Itoa(obj.ID)
	}

	if obj.Metadata.Workspace == "" {
		return "default-staticcluster-" + strconv.Itoa(obj.ID) + "-" + obj.Metadata.Name
	}

	return obj.Metadata.Workspace + "-staticcluster-" + strconv.Itoa(obj.ID) + "-" + obj.Metadata.Name
}

func (obj *StaticCluster) GetName() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Name
}

func (obj *StaticCluster) GetWorkspace() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Workspace
}

func (obj *StaticCluster) GetLabels() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Labels
}

func (obj *StaticCluster) SetLabels(labels map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Labels = labels
}

func (obj *StaticCluster) GetAnnotations() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Annotations
}

func (obj *StaticCluster) SetAnnotations(annotations map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Annotations = annotations
}

func (obj *StaticCluster) GetCreationTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.CreationTimestamp
}

func (obj *StaticCluster) GetUpdateTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.UpdateTimestamp
}

func (obj *StaticCluster) GetDeletionTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.DeletionTimestamp
}

func (obj *StaticCluster) GetSpec() interface{} {
	return obj.Spec
}

func (obj *StaticCluster) GetStatus() interface{} {
	return obj.Status
}

func (obj *StaticCluster) GetKind() string {
	return obj.Kind
}

func (obj *StaticCluster) SetKind(kind string) {
	obj.Kind = kind
}

func (obj *StaticCluster) GetID() string {
	return strconv.Itoa(obj.ID)
}

func (obj *StaticCluster) GetMetadata() interface{} {
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

type StaticClusterList struct {
	Kind  string          `json:"kind"`
	Items []StaticCluster `json:"items"`
}

func (in *StaticClusterList) GetKind() string {
	return in.Kind
}

func (in *StaticClusterList) SetKind(kind string) {
	in.Kind = kind
}

func (in *StaticClusterList) GetItems() []scheme.Object {
	objs := make([]scheme.Object, 0, len(in.Items))
	for i := range in.Items {
		objs = append(objs, &in.Items[i])
	}

	return objs
}

func (in *StaticClusterList) SetItems(objs []scheme.Object) {
	items := make([]StaticCluster, len(objs))
	for i, obj := range objs {
		items[i] = *obj.(*StaticCluster) //nolint:errcheck
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
