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
	// Info carries display-only metadata about the model itself (parameter
	// count, quantization, context length, architecture). It never
	// participates in deployment composition — same advisory nature as the
	// hardware-verified annotation. The model catalog card / show page renders
	// it per variant. Optional and forward-compatible; legacy specs omit it.
	Info *ModelInfo `json:"info,omitempty"`
}

// ModelInfo is display-only metadata describing the model checkpoint a variant
// points at. It belongs to the model (not the catalog template), so it lives on
// ModelSpec and is reused wherever a model is referenced. When the dedicated
// model repository resource lands it can populate this same shape.
type ModelInfo struct {
	ParameterCount string `json:"parameter_count,omitempty"` // e.g. "72.7B"
	Quantization   string `json:"quantization,omitempty"`    // e.g. "bf16" / "fp8"
	ContextLength  string `json:"context_length,omitempty"`  // e.g. "32K" or token count
	Architecture   string `json:"architecture,omitempty"`    // e.g. "dense" / "moe"
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
	Cluster           string              `json:"cluster,omitempty"`
	Model             *ModelSpec          `json:"model,omitempty"`
	Engine            *EndpointEngineSpec `json:"engine,omitempty"`
	Resources         *ResourceSpec       `json:"resources,omitempty"`
	Replicas          ReplicaSpec         `json:"replicas,omitempty"`
	DeploymentOptions map[string]any      `json:"deployment_options,omitempty"`
	Variables         map[string]any      `json:"variables,omitempty"`
	Env               map[string]string   `json:"env,omitempty"`

	// Recipe reference: when ModelCatalog is set and Model is nil the
	// endpoint controller resolves the catalog, composes the kernel, writes
	// the result into the legacy fields above, and clears these refs as a
	// "already expanded" marker so downstream controllers stay untouched.
	ModelCatalog    string   `json:"model_catalog,omitempty"`
	Variant         string   `json:"variant,omitempty"`
	EnabledFeatures []string `json:"enabled_features,omitempty"`
}

type EndpointPhase string

const (
	EndpointPhasePENDING          EndpointPhase = "Pending"
	EndpointPhaseRUNNING          EndpointPhase = "Running"
	EndpointPhaseFAILED           EndpointPhase = "Failed"
	EndpointPhaseDELETED          EndpointPhase = "Deleted"
	EndpointPhasePAUSED           EndpointPhase = "Paused"
	EndpointPhaseDEPLOYING        EndpointPhase = "Deploying"
	EndpointPhaseMODELDOWNLOADING EndpointPhase = "ModelDownloading"
	EndpointPhaseDELETING         EndpointPhase = "Deleting"
)

type EndpointStatus struct {
	Phase                      EndpointPhase           `json:"phase,omitempty"`
	ServiceURL                 string                  `json:"service_url,omitempty"`
	LastTransitionTime         string                  `json:"last_transition_time,omitempty"`
	ErrorMessage               string                  `json:"error_message,omitempty"`
	ModelDownloadCompletedHash *string                 `json:"model_download_completed_hash,omitempty"`
	Resources                  *EndpointResourceStatus `json:"resources,omitempty"`
}

type EndpointResourceStatus struct {
	Replicas []ReplicaDeviceAllocation `json:"replicas,omitempty"`
	Summary  *EndpointResourceSummary  `json:"summary,omitempty"`
}

type ReplicaDeviceAllocation struct {
	InstanceID string             `json:"instance_id"`
	ReplicaID  string             `json:"replica_id,omitempty"`
	NodeID     string             `json:"node_id,omitempty"`
	Devices    []DeviceAllocation `json:"devices,omitempty"`
}

type DeviceAllocation struct {
	UUID      string `json:"uuid"`
	Product   string `json:"product"`
	MemoryMiB int64  `json:"memory_mib"`
	CoreUnits int64  `json:"core_units"`
	NodeID    string `json:"node_id"`
}

type EndpointResourceSummary struct {
	Products map[AcceleratorProduct]*ProductUsage `json:"products,omitempty"`
}

type ProductUsage struct {
	MemoryMiB int64 `json:"memory_mib"`
	CoreUnits int64 `json:"core_units"`
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

func (obj *Endpoint) GetSpec() any {
	return obj.Spec
}

func (obj *Endpoint) GetStatus() any {
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

func (obj *Endpoint) GetMetadata() any {
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
