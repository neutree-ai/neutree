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
	CPU         *float64           `json:"cpu,omitempty"`
	GPU         *float64           `json:"gpu,omitempty"`
	Accelerator map[string]float64 `json:"accelerator,omitempty"`
	Memory      *float64           `json:"memory,omitempty"`
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
}

type EndpointPhase string

const (
	EndpointPhasePENDING EndpointPhase = "Pending"
	EndpointPhaseRUNNING EndpointPhase = "Running"
	EndpointPhaseFAILED  EndpointPhase = "Failed"
	EndpointPhaseDELETED EndpointPhase = "Deleted"
)

type EndpointStatus struct {
	Phase              EndpointPhase `json:"phase,omitempty"`
	ServiceURL         string        `json:"service_url,omitempty"`
	LastTransitionTime string        `json:"last_transition_time,omitempty"`
	ErrorMessage       string        `json:"error_message,omitempty"`
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

func (obj *Endpoint) SetName(name string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Name = name
}

func (obj *Endpoint) GetWorkspace() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Workspace
}

func (obj *Endpoint) SetWorkspace(workspace string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Workspace = workspace
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

func (obj *Endpoint) SetCreationTimestamp(timestamp string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.CreationTimestamp = timestamp
}

func (obj *Endpoint) GetUpdateTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.UpdateTimestamp
}

func (obj *Endpoint) SetUpdateTimestamp(timestamp string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.UpdateTimestamp = timestamp
}

func (obj *Endpoint) GetDeletionTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.DeletionTimestamp
}

func (obj *Endpoint) SetDeletionTimestamp(timestamp string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.DeletionTimestamp = timestamp
}

func (obj *Endpoint) GetSpec() interface{} {
	return obj.Spec
}

func (obj *Endpoint) SetSpec(spec interface{}) {
	obj.Spec = spec.(*EndpointSpec) //nolint:errcheck
}

func (obj *Endpoint) GetStatus() interface{} {
	return obj.Status
}

func (obj *Endpoint) SetStatus(status interface{}) {
	obj.Status = status.(*EndpointStatus) //nolint:errcheck
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

func (obj *Endpoint) SetMetadata(m interface{}) {
	obj.Metadata = m.(*Metadata) //nolint:errcheck
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
