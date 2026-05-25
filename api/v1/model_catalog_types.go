package v1

import (
	"strconv"

	"github.com/neutree-ai/neutree/pkg/scheme"
)

type ModelCatalogPhase string

const (
	ModelCatalogPhasePENDING ModelCatalogPhase = "Pending"
	ModelCatalogPhaseREADY   ModelCatalogPhase = "Ready"
	ModelCatalogPhaseFAILED  ModelCatalogPhase = "Failed"
	ModelCatalogPhaseDELETED ModelCatalogPhase = "Deleted"
)

type ModelCatalogSpec struct {
	Model             *ModelSpec             `json:"model,omitempty"`
	Engine            *EndpointEngineSpec    `json:"engine,omitempty"`
	Resources         *ResourceSpec          `json:"resources,omitempty"`
	Replicas          *ReplicaSpec           `json:"replicas,omitempty"`
	DeploymentOptions map[string]any `json:"deployment_options,omitempty"`
	Variables         map[string]any `json:"variables,omitempty"`
	Env               map[string]string      `json:"env,omitempty"`

	// Recipe extension: when Variants is non-empty the catalog is a recipe
	// template; ComposeEndpointSpec selects a variant and merges enabled
	// features on top of Base to produce a concrete endpoint kernel.
	Base     *RecipeBase              `json:"base,omitempty"`
	Variants map[string]RecipeVariant `json:"variants,omitempty"`
	Features map[string]RecipeFeature `json:"features,omitempty"`
}

// RecipeBase carries config shared by every variant in a recipe MC.
type RecipeBase struct {
	EngineArgs map[string]any `json:"engine_args,omitempty"`
	Env        map[string]string      `json:"env,omitempty"`
}

// RecipeVariant is what differs per variant: typically the checkpoint
// (model) and hardware footprint (resources); engine_args/env overrides are
// allowed but optional.
type RecipeVariant struct {
	Model       *ModelSpec             `json:"model,omitempty"`
	Resources   *ResourceSpec          `json:"resources,omitempty"`
	EngineArgs  map[string]any `json:"engine_args,omitempty"`
	Env         map[string]string      `json:"env,omitempty"`
	Description string                 `json:"description,omitempty"`
}

// RecipeFeature is an independently toggleable bundle of engine_args/env.
type RecipeFeature struct {
	Description   string                 `json:"description,omitempty"`
	Default       bool                   `json:"default,omitempty"`
	EngineArgs    map[string]any `json:"engine_args,omitempty"`
	Env           map[string]string      `json:"env,omitempty"`
	ConflictsWith []string               `json:"conflicts_with,omitempty"`
}

type ModelCatalogStatus struct {
	Phase              ModelCatalogPhase `json:"phase,omitempty"`
	LastTransitionTime string            `json:"last_transition_time,omitempty"`
	ErrorMessage       string            `json:"error_message,omitempty"`
}

type ModelCatalog struct {
	APIVersion string              `json:"api_version,omitempty"`
	ID         int                 `json:"id,omitempty"`
	Kind       string              `json:"kind,omitempty"`
	Metadata   *Metadata           `json:"metadata,omitempty"`
	Spec       *ModelCatalogSpec   `json:"spec,omitempty"`
	Status     *ModelCatalogStatus `json:"status,omitempty"`
}

func (r ModelCatalog) Key() string {
	if r.Metadata == nil {
		return "default" + "-" + "modelcatalog" + "-" + strconv.Itoa(r.ID)
	}

	if r.Metadata.Workspace == "" {
		return "default" + "-" + "modelcatalog" + "-" + strconv.Itoa(r.ID) + "-" + r.Metadata.Name
	}

	return r.Metadata.Workspace + "-" + "modelcatalog" + "-" + strconv.Itoa(r.ID) + "-" + r.Metadata.Name
}

func (obj *ModelCatalog) GetName() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Name
}

func (obj *ModelCatalog) GetWorkspace() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Workspace
}

func (obj *ModelCatalog) GetLabels() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Labels
}

func (obj *ModelCatalog) SetLabels(labels map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Labels = labels
}

func (obj *ModelCatalog) GetAnnotations() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Annotations
}

func (obj *ModelCatalog) SetAnnotations(annotations map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Annotations = annotations
}

func (obj *ModelCatalog) GetCreationTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.CreationTimestamp
}

func (obj *ModelCatalog) GetUpdateTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.UpdateTimestamp
}

func (obj *ModelCatalog) GetDeletionTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.DeletionTimestamp
}

func (obj *ModelCatalog) GetSpec() any {
	return obj.Spec
}

func (obj *ModelCatalog) GetStatus() any {
	return obj.Status
}

func (obj *ModelCatalog) GetKind() string {
	return obj.Kind
}

func (obj *ModelCatalog) SetKind(kind string) {
	obj.Kind = kind
}

func (obj *ModelCatalog) GetID() string {
	return strconv.Itoa(obj.ID)
}

func (obj *ModelCatalog) SetID(id string) {
	obj.ID, _ = strconv.Atoi(id)
}

func (obj *ModelCatalog) GetMetadata() any {
	return obj.Metadata
}

// ModelCatalogList is a list of ModelCatalog resources
type ModelCatalogList struct {
	Kind  string         `json:"kind"`
	Items []ModelCatalog `json:"items"`
}

func (in *ModelCatalogList) GetKind() string {
	return in.Kind
}

func (in *ModelCatalogList) SetKind(kind string) {
	in.Kind = kind
}

func (in *ModelCatalogList) GetItems() []scheme.Object {
	var objs []scheme.Object
	for i := range in.Items {
		objs = append(objs, &in.Items[i])
	}

	return objs
}

func (in *ModelCatalogList) SetItems(objs []scheme.Object) {
	items := make([]ModelCatalog, len(objs))
	for i, obj := range objs {
		items[i] = *obj.(*ModelCatalog) //nolint:errcheck
	}

	in.Items = items
}
