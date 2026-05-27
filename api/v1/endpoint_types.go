package v1

import (
	"strconv"

	"github.com/neutree-ai/neutree/pkg/scheme"
)

type ModelSpec struct {
	Registry string `json:"registry,omitempty"`
	Name     string `json:"name,omitempty"`
	File     string `json:"file,omitempty"`
	Version  string `json:"version,omitempty"`
	Task     string `json:"task,omitempty"`
}

type EndpointEngineSpec struct {
	Engine  string `json:"engine,omitempty"`
	Version string `json:"version,omitempty"`
}

type ResourceSpec struct {
	CPU         *string           `json:"cpu,omitempty"`
	GPU         *string           `json:"gpu,omitempty"`
	Accelerator map[string]string `json:"accelerator,omitempty"`
	Memory      *string           `json:"memory,omitempty"`
}

type ReplicaSpec struct {
	Num *int `json:"num,omitempty"`
}

type EndpointSpec struct {
	Cluster           string                 `json:"cluster,omitempty"`
	Model             *ModelSpec             `json:"model,omitempty"`
	Engine            *EndpointEngineSpec    `json:"engine,omitempty"`
	Resources         *ResourceSpec          `json:"resources,omitempty"`
	Replicas          ReplicaSpec            `json:"replicas,omitempty"`
	DeploymentOptions map[string]interface{} `json:"deployment_options,omitempty"`
	Variables         map[string]interface{} `json:"variables,omitempty"`
	Env               map[string]string      `json:"env,omitempty"`

	// PD same-host fields. Empty strategy follows the existing standard path;
	// strategy="pd" enables prefill / decode role-group deployment.
	Strategy  string             `json:"strategy,omitempty"`  // "standard" | "pd"
	Placement *PlacementSpec     `json:"placement,omitempty"` // dual-axis placement constraint
	Roles     []EndpointRoleSpec `json:"roles,omitempty"`
	KV        *KVSpec            `json:"kv,omitempty"`
}

// KVSpec is the endpoint-level KV data-plane config container. Phase 1 only
// exposes per-request P/D transfer; cache/offload can be added later without
// mixing it into the transfer contract.
type KVSpec struct {
	Transfer *KVTransferSpec `json:"transfer,omitempty"`
}

// KVTransferSpec describes the prefill -> decode KV transfer for the current
// request. Connector defaults are derived by placement profile when empty.
type KVTransferSpec struct {
	Connector string                 `json:"connector,omitempty"`
	Extra     map[string]interface{} `json:"extra,omitempty"`
}

type EndpointPhase string

const (
	EndpointPhasePENDING   EndpointPhase = "Pending"
	EndpointPhaseRUNNING   EndpointPhase = "Running"
	EndpointPhaseFAILED    EndpointPhase = "Failed"
	EndpointPhaseDELETED   EndpointPhase = "Deleted"
	EndpointPhasePAUSED    EndpointPhase = "Paused"
	EndpointPhaseDEPLOYING EndpointPhase = "Deploying"
	EndpointPhaseDELETING  EndpointPhase = "Deleting"
)

type EndpointStatus struct {
	Phase              EndpointPhase `json:"phase,omitempty"`
	ServiceURL         string        `json:"service_url,omitempty"`
	LastTransitionTime string        `json:"last_transition_time,omitempty"`
	ErrorMessage       string        `json:"error_message,omitempty"`

	// PD same-host status fields. Replica counters are RoleGroup counts, not
	// prefill + decode actor totals.
	Strategy      string          `json:"strategy,omitempty"`
	Placement     string          `json:"placement,omitempty"`
	Replicas      []ReplicaStatus `json:"replicas,omitempty"`
	TotalReplicas int             `json:"total_replicas,omitempty"`
	ReadyReplicas int             `json:"ready_replicas,omitempty"`
}

type Endpoint struct {
	ID         int             `json:"id,omitempty"`
	APIVersion string          `json:"api_version,omitempty"`
	Kind       string          `json:"kind,omitempty"`
	Metadata   *Metadata       `json:"metadata,omitempty"`
	Spec       *EndpointSpec   `json:"spec,omitempty"`
	Status     *EndpointStatus `json:"status,omitempty"`
}

func (e Endpoint) Key() string {
	if e.Metadata == nil {
		return "default" + "-" + "endpint" + "-" + strconv.Itoa(e.ID)
	}

	if e.Metadata.Workspace == "" {
		return "default" + "-" + "endpint" + "-" + strconv.Itoa(e.ID) + "-" + e.Metadata.Name
	}

	return e.Metadata.Workspace + "-" + "endpint" + "-" + strconv.Itoa(e.ID) + "-" + e.Metadata.Name
}

func (obj *Endpoint) GetName() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Name
}

func (obj *Endpoint) GetWorkspace() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Workspace
}

func (obj *Endpoint) GetLabels() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Labels
}

func (obj *Endpoint) SetLabels(labels map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Labels = labels
}

func (obj *Endpoint) GetAnnotations() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Annotations
}

func (obj *Endpoint) SetAnnotations(annotations map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Annotations = annotations
}

func (obj *Endpoint) GetCreationTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.CreationTimestamp
}

func (obj *Endpoint) GetUpdateTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.UpdateTimestamp
}

func (obj *Endpoint) GetDeletionTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.DeletionTimestamp
}

func (obj *Endpoint) GetSpec() interface{} {
	return obj.Spec
}

func (obj *Endpoint) GetStatus() interface{} {
	return obj.Status
}

func (obj *Endpoint) GetKind() string {
	return obj.Kind
}

func (obj *Endpoint) SetKind(kind string) {
	obj.Kind = kind
}

func (obj *Endpoint) GetID() string {
	return strconv.Itoa(obj.ID)
}

func (obj *Endpoint) SetID(id string) {
	obj.ID, _ = strconv.Atoi(id)
}

func (obj *Endpoint) GetMetadata() interface{} {
	return obj.Metadata
}

// EndpointList is a list of Endpoint resources
type EndpointList struct {
	Kind  string     `json:"kind"`
	Items []Endpoint `json:"items"`
}

func (in *EndpointList) GetKind() string {
	return in.Kind
}

func (in *EndpointList) SetKind(kind string) {
	in.Kind = kind
}

func (in *EndpointList) GetItems() []scheme.Object {
	var objs []scheme.Object
	for i := range in.Items {
		objs = append(objs, &in.Items[i])
	}

	return objs
}

func (in *EndpointList) SetItems(objs []scheme.Object) {
	items := make([]Endpoint, len(objs))
	for i, obj := range objs {
		items[i] = *obj.(*Endpoint) //nolint:errcheck
	}

	in.Items = items
}
