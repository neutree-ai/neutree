package v1

import (
	"strconv"

	corev1 "k8s.io/api/core/v1"

	"github.com/neutree-ai/neutree/pkg/scheme"
)

// Neutree node provision status.
const (
	ProvisioningNodeProvisionStatus = "provisioning"
	ProvisionedNodeProvisionStatus  = "provisioned"
)

type Cluster struct {
	ID         int            `json:"id,omitempty"`
	APIVersion string         `json:"api_version,omitempty"`
	Kind       string         `json:"kind,omitempty"`
	Metadata   *Metadata      `json:"metadata,omitempty"`
	Spec       *ClusterSpec   `json:"spec,omitempty"`
	Status     *ClusterStatus `json:"status,omitempty"`
}

type ClusterSpec struct {
	// current only support "ssh"
	Type          string `json:"type"`
	Config        any    `json:"config"`
	ImageRegistry string `json:"image_registry"`
	// the neutree serving version, if not specified, the default version will be used
	Version string `json:"version"`
}

type RaySSHProvisionClusterConfig struct {
	Provider Provider `json:"provider,omitempty" yaml:"provider,omitempty"`
	Auth     Auth     `json:"auth,omitempty" yaml:"auth,omitempty"`

	CommonClusterConfig `json:",inline" yaml:",inline"`
}

type RayKubernetesProvisionClusterConfig struct {
	Kubeconfig       string            `json:"kubeconfig,omitempty" yaml:"kubeconfig,omitempty"`
	HeadNodeSpec     HeadNodeSpec      `json:"head_node_spec,omitempty" yaml:"head_node_spec,omitempty"`
	WorkerGroupSpecs []WorkerGroupSpec `json:"worker_group_specs,omitempty" yaml:"worker_group_specs,omitempty"`

	CommonClusterConfig `json:",inline" yaml:",inline"`
}

type CommonClusterConfig struct {
	// todo: after heterogeneous accelerator hybrid clusters are supported, this field will be deprecated.
	AcceleratorType *string `json:"accelerator_type,omitempty" yaml:"accelerator_type,omitempty"`
	// ModelCache is used to cache models downloaded from remote model registries, such as huggingface hub, bentoml cloud, etc.
	// It does not apply to local model registries, such as bentoml nfs/local dir type.
	// In addition, other data may be cached, which depends on the corresponding model registry download implementation,
	// so it is not recommended to share a storage with the local model registry.
	ModelCaches []ModelCache `json:"model_caches,omitempty" yaml:"model_caches,omitempty"`
}

type KubernetesAccessMode string

const (
	KubernetesAccessModeLoadBalancer KubernetesAccessMode = "LoadBalancer"
	// todo support ingress access mode.
	KubernetesAccessModeIngress KubernetesAccessMode = "Ingress"
)

type HeadNodeSpec struct {
	AccessMode KubernetesAccessMode `json:"access_mode,omitempty" yaml:"access_mode,omitempty"`
	Resources  map[string]string    `json:"resources,omitempty" yaml:"resources,omitempty"`
}

type WorkerGroupSpec struct {
	GroupName   string            `json:"group_name,omitempty" yaml:"group_name,omitempty"`
	MinReplicas int32             `json:"min_replicas,omitempty" yaml:"min_replicas,omitempty"`
	MaxReplicas int32             `json:"max_replicas,omitempty" yaml:"max_replicas,omitempty"`
	Resources   map[string]string `json:"resources,omitempty" yaml:"resources,omitempty"`
}

type ModelCache struct {
	ModelRegistryType ModelRegistryType            `json:"model_registry_type,omitempty" yaml:"model_registry_type,omitempty"`
	HostPath          *corev1.HostPathVolumeSource `json:"host_path,omitempty" yaml:"host_path,omitempty"`
	// Only Kubernetes type cluster support NFS.
	NFS *corev1.NFSVolumeSource `json:"nfs,omitempty" yaml:"nfs,omitempty"`
	// todo: support other model cache type, e.g. pvc etc.
}

type ClusterStatus struct {
	Phase              ClusterPhase `json:"phase,omitempty"`
	Image              string       `json:"image,omitempty"`
	DashboardURL       string       `json:"dashboard_url,omitempty"`
	LastTransitionTime string       `json:"last_transition_time,omitempty"`
	ErrorMessage       string       `json:"error_message,omitempty"`
	// the number of ready nodes in the cluster.
	ReadyNodes int `json:"ready_nodes,omitempty"`
	// the desired number of nodes in the cluster.
	DesiredNodes int `json:"desired_nodes,omitempty"`
	// the current neutree serving version, it will be set as the minimum version of the nodes in the cluster.
	Version string `json:"version,omitempty"`
	// ray version.
	RayVersion string `json:"ray_version,omitempty"`
	// whether the cluster is initialized.
	Initialized bool `json:"initialized,omitempty"`
	// the cluster all node provision status.
	// current only record the static node provision status.
	NodeProvisionStatus string `json:"node_provision_status,omitempty"`
	// the resource information of the cluster.
	ResourceInfo map[string]float64 `json:"resource_info,omitempty"`
}

func (c Cluster) Key() string {
	if c.Metadata == nil {
		return "default" + "-" + "clsuter" + "-" + strconv.Itoa(c.ID)
	}

	if c.Metadata.Workspace == "" {
		return "default" + "-" + "clsuter" + "-" + strconv.Itoa(c.ID) + "-" + c.Metadata.Name
	}

	return c.Metadata.Workspace + "-" + "clsuter" + "-" + strconv.Itoa(c.ID) + "-" + c.Metadata.Name
}

func (c Cluster) IsInitialized() bool {
	if c.Status == nil {
		return false
	}

	return c.Status.Initialized
}

type ClusterPhase string

const (
	ClusterPhasePending ClusterPhase = "Pending"
	ClusterPhaseRunning ClusterPhase = "Running"
	ClusterPhaseFailed  ClusterPhase = "Failed"
	ClusterPhaseDeleted ClusterPhase = "Deleted"
)

func (obj *Cluster) GetName() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Name
}

func (obj *Cluster) SetName(name string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Name = name
}

func (obj *Cluster) GetWorkspace() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Workspace
}

func (obj *Cluster) SetWorkspace(workspace string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Workspace = workspace
}

func (obj *Cluster) GetLabels() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Labels
}

func (obj *Cluster) SetLabels(labels map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Labels = labels
}

func (obj *Cluster) GetAnnotations() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Annotations
}

func (obj *Cluster) SetAnnotations(annotations map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Annotations = annotations
}

func (obj *Cluster) GetCreationTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.CreationTimestamp
}

func (obj *Cluster) SetCreationTimestamp(timestamp string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.CreationTimestamp = timestamp
}

func (obj *Cluster) GetUpdateTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.UpdateTimestamp
}

func (obj *Cluster) SetUpdateTimestamp(timestamp string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.UpdateTimestamp = timestamp
}

func (obj *Cluster) GetDeletionTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.DeletionTimestamp
}

func (obj *Cluster) SetDeletionTimestamp(timestamp string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.DeletionTimestamp = timestamp
}

func (obj *Cluster) GetSpec() interface{} {
	return obj.Spec
}

func (obj *Cluster) SetSpec(spec interface{}) {
	obj.Spec = spec.(*ClusterSpec) //nolint:errcheck
}

func (obj *Cluster) GetStatus() interface{} {
	return obj.Status
}

func (obj *Cluster) SetStatus(status interface{}) {
	obj.Status = status.(*ClusterStatus) //nolint:errcheck
}

func (obj *Cluster) GetKind() string {
	return obj.Kind
}

func (obj *Cluster) SetKind(kind string) {
	obj.Kind = kind
}

func (obj *Cluster) GetID() string {
	return strconv.Itoa(obj.ID)
}

func (obj *Cluster) GetMetadata() interface{} {
	return obj.Metadata
}

func (obj *Cluster) SetMetadata(m interface{}) {
	obj.Metadata = m.(*Metadata) //nolint:errcheck
}

// ClusterList is a list of Cluster resources
type ClusterList struct {
	Kind  string    `json:"kind"`
	Items []Cluster `json:"items"`
}

func (in *ClusterList) GetKind() string {
	return in.Kind
}

func (in *ClusterList) SetKind(kind string) {
	in.Kind = kind
}

func (in *ClusterList) GetItems() []scheme.Object {
	var objs []scheme.Object
	for i := range in.Items {
		objs = append(objs, &in.Items[i])
	}

	return objs
}

func (in *ClusterList) SetItems(objs []scheme.Object) {
	items := make([]Cluster, len(objs))
	for i, obj := range objs {
		items[i] = *obj.(*Cluster) //nolint:errcheck
	}

	in.Items = items
}
