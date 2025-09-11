package v1

import (
	"strconv"

	"github.com/neutree-ai/neutree/pkg/scheme"
)

const (
	TextGenerationModelTask = "text-generation"
	TextEmbeddingModelTask  = "text-embedding"
	TextRerankModelTask     = "text-rerank"
)

type EngineVersion struct {
	Version      string                 `json:"version,omitempty"`
	ValuesSchema map[string]interface{} `json:"values_schema,omitempty"`
}

type EngineSpec struct {
	Versions       []*EngineVersion `json:"versions,omitempty"`
	SupportedTasks []string         `json:"supported_tasks,omitempty"`
}

type EnginePhase string

const (
	EnginePhasePending EnginePhase = "Pending"
	EnginePhaseCreated EnginePhase = "Created"
	EnginePhaseDeleted EnginePhase = "Deleted"
	EnginePhaseFailed  EnginePhase = "Failed"
)

type EngineStatus struct {
	Phase              EnginePhase `json:"phase,omitempty"`
	LastTransitionTime string      `json:"last_transition_time,omitempty"`
	ErrorMessage       string      `json:"error_message,omitempty"`
}

type Engine struct {
	ID         int           `json:"id,omitempty"`
	APIVersion string        `json:"api_version,omitempty"`
	Kind       string        `json:"kind,omitempty"`
	Metadata   *Metadata     `json:"metadata,omitempty"`
	Spec       *EngineSpec   `json:"spec,omitempty"`
	Status     *EngineStatus `json:"status,omitempty"`
}

func (obj *Engine) GetName() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Name
}

func (obj *Engine) GetWorkspace() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Workspace
}

func (obj *Engine) GetLabels() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Labels
}

func (obj *Engine) SetLabels(labels map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Labels = labels
}

func (obj *Engine) GetAnnotations() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Annotations
}

func (obj *Engine) SetAnnotations(annotations map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Annotations = annotations
}

func (obj *Engine) GetCreationTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.CreationTimestamp
}

func (obj *Engine) GetUpdateTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.UpdateTimestamp
}

func (obj *Engine) GetDeletionTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.DeletionTimestamp
}

func (obj *Engine) GetSpec() interface{} {
	return obj.Spec
}

func (obj *Engine) GetStatus() interface{} {
	return obj.Status
}

func (obj *Engine) GetKind() string {
	return obj.Kind
}

func (obj *Engine) SetKind(kind string) {
	obj.Kind = kind
}

func (obj *Engine) GetID() string {
	return strconv.Itoa(obj.ID)
}

func (obj *Engine) SetID(id string) {
	obj.ID, _ = strconv.Atoi(id)
}

func (obj *Engine) GetMetadata() interface{} {
	return obj.Metadata
}

// EngineList is a list of Engine resources
type EngineList struct {
	Kind  string   `json:"kind"`
	Items []Engine `json:"items"`
}

func (in *EngineList) GetKind() string {
	return in.Kind
}

func (in *EngineList) SetKind(kind string) {
	in.Kind = kind
}

func (in *EngineList) GetItems() []scheme.Object {
	var objs []scheme.Object
	for i := range in.Items {
		objs = append(objs, &in.Items[i])
	}

	return objs
}

func (in *EngineList) SetItems(objs []scheme.Object) {
	items := make([]Engine, len(objs))
	for i, obj := range objs {
		items[i] = *obj.(*Engine) //nolint:errcheck
	}

	in.Items = items
}
