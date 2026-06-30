package v1

import (
	"encoding/json"
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
	Model             *ModelSpec          `json:"model,omitempty"`
	Engine            *EndpointEngineSpec `json:"engine,omitempty"`
	Resources         *ResourceSpec       `json:"resources,omitempty"`
	Replicas          *ReplicaSpec        `json:"replicas,omitempty"`
	DeploymentOptions map[string]any      `json:"deployment_options,omitempty"`
	Variables         map[string]any      `json:"variables,omitempty"`
	Env               map[string]string   `json:"env,omitempty"`

	// Recipe extension: when Variants is non-empty the catalog is a recipe
	// template; ComposeEndpointSpec selects a variant and merges enabled
	// features on top of Base to produce a concrete endpoint kernel.
	Base     *RecipeBase              `json:"base,omitempty"`
	Variants map[string]RecipeVariant `json:"variants,omitempty"`
	// Features is an ordered list: list position is the UI display order, and
	// items are grouped into sections by RecipeFeature.Group. (A map cannot
	// carry order — Go marshals map keys sorted, and the catalog reconcile
	// re-serializes spec, so any insertion order would be lost.)
	Features []RecipeFeature `json:"features,omitempty"`
}

// RecipeBase carries config shared by every variant in a recipe MC.
type RecipeBase struct {
	EngineArgs map[string]any    `json:"engine_args,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
}

// RecipeVariant is what differs per variant: typically the checkpoint
// (model) and hardware footprint (resources); engine_args/env overrides are
// allowed but optional. `VRAMMinimumGB` mirrors upstream and feeds the
// "needs ≥ X GB" hint plus the OOM-risk badge at endpoint creation.
type RecipeVariant struct {
	Model         *ModelSpec        `json:"model,omitempty"`
	Resources     *ResourceSpec     `json:"resources,omitempty"`
	EngineArgs    map[string]any    `json:"engine_args,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	Description   string            `json:"description,omitempty"`
	VRAMMinimumGB *int              `json:"vram_minimum_gb,omitempty"`
}

// RecipeFeatureType discriminates how a feature is selected and composed.
// Empty is treated as "boolean" (the original on/off toggle).
type RecipeFeatureType string

const (
	RecipeFeatureTypeBoolean RecipeFeatureType = "boolean"
	RecipeFeatureTypeSelect  RecipeFeatureType = "select"
	RecipeFeatureTypeInput   RecipeFeatureType = "input"
)

// RecipeFeature is a recipe configuration knob with three shapes selected by
// Type:
//   - boolean (default): an on/off bundle of engine_args/env, merged when on.
//   - select:  pick one of Options; the chosen option's engine_args/env merge
//     on top of the feature's shared engine_args/env.
//   - input:   a free value substituted for the "${value}" placeholder inside
//     the feature's engine_args/env (coerced per Input.ValueType).
//
// Features live in spec.Features as an ordered list; the list position is the
// authoritative display order (a map would lose it — Go marshals map keys
// sorted, which the reconcile re-serialize would impose). `Group` names the UI
// section a feature belongs to; sections render in first-seen order, features
// within a section keep list order. Neither Name nor Group affect composition.
type RecipeFeature struct {
	// Name is the feature's stable identifier (referenced by FeatureSelection
	// and ConflictsWith). It was the map key before features became a list.
	Name string `json:"name"`
	// Group is the UI section label this feature renders under (e.g. "Core
	// parameters" / "Performance tuning"); empty means the default section.
	// Features sharing a Group render together; sections appear in first-seen
	// order. No effect on composition. Supersedes the former free-form
	// `category` hint.
	Group string `json:"group,omitempty"`
	// DisplayName is an optional human-facing label for the UI (e.g. "Context
	// window"); when empty the feature Name is shown. Lets a
	// catalog align feature labels with product wording, independent of the
	// technical feature name. No effect on composition.
	DisplayName   string   `json:"display_name,omitempty"`
	Description   string   `json:"description,omitempty"`
	ConflictsWith []string `json:"conflicts_with,omitempty"`

	// Type selects the feature shape; empty == "boolean".
	Type RecipeFeatureType `json:"type,omitempty"`

	// boolean (Type empty or "boolean")
	Default    bool              `json:"default,omitempty"`
	EngineArgs map[string]any    `json:"engine_args,omitempty"`
	Env        map[string]string `json:"env,omitempty"`

	// select (Type "select")
	Options       map[string]RecipeFeatureOption `json:"options,omitempty"`
	DefaultOption string                         `json:"default_option,omitempty"`

	// input (Type "input")
	Input *RecipeFeatureInput `json:"input,omitempty"`
}

// RecipeFeatureOption is one choice of a select feature. Its engine_args/env
// merge on top of the feature's shared engine_args/env when chosen.
type RecipeFeatureOption struct {
	Description string            `json:"description,omitempty"`
	EngineArgs  map[string]any    `json:"engine_args,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
}

// RecipeFeatureInput describes a free-input feature. The user value replaces
// every "${value}" placeholder inside the feature's engine_args/env; when an
// engine_args value is exactly "${value}" it is coerced to ValueType (so e.g.
// max_model_len comes out as a number, not a string).
type RecipeFeatureInput struct {
	ValueType string   `json:"value_type,omitempty"` // "string" (default) | "int" | "number" | "bool"
	Default   string   `json:"default,omitempty"`
	Required  bool     `json:"required,omitempty"`
	Min       *float64 `json:"min,omitempty"`
	Max       *float64 `json:"max,omitempty"`
	Pattern   string   `json:"pattern,omitempty"` // regexp for string values
	Enum      []string `json:"enum,omitempty"`    // allowed raw values
	// Suggestions are preset values surfaced as a dropdown next to the free
	// input (the "pick a preset or type your own" combobox). UI hint only —
	// the user may still enter any value that satisfies the constraints above.
	// Each entry may carry an optional display Label (e.g. show "8K" for value
	// "8192"); the composed value is always the raw Value, never the label.
	Suggestions []RecipeFeatureSuggestion `json:"suggestions,omitempty"`
}

// RecipeFeatureSuggestion is one preset in an input feature's combobox. It
// accepts two JSON shapes for back-compat: a bare string (the value, no label)
// or an object {value, label}. The composed/validated value is always Value;
// Label is purely a display hint (e.g. "8K" for "8192"). To keep reconcile
// re-serialization stable, it marshals back to a bare string when Label is
// empty.
type RecipeFeatureSuggestion struct {
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
}

func (s *RecipeFeatureSuggestion) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		return json.Unmarshal(b, &s.Value)
	}

	type alias RecipeFeatureSuggestion

	return json.Unmarshal(b, (*alias)(s))
}

func (s RecipeFeatureSuggestion) MarshalJSON() ([]byte, error) {
	if s.Label == "" {
		return json.Marshal(s.Value)
	}

	type alias RecipeFeatureSuggestion

	return json.Marshal(alias(s))
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
