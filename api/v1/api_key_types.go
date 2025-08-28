package v1

import (
	"github.com/neutree-ai/neutree/pkg/scheme"
)

type ApiKeySpec struct {
	Quota int64 `json:"quota,omitempty"`
}

type ApiKeyPhase string

const (
	ApiKeyPhasePENDING ApiKeyPhase = "Pending"
	ApiKeyPhaseCREATED ApiKeyPhase = "Created"
	ApiKeyPhaseDELETED ApiKeyPhase = "Deleted"
)

type ApiKeyStatus struct {
	ErrorMessage       string      `json:"error_message,omitempty"`
	LastTransitionTime string      `json:"last_transition_time,omitempty"`
	Phase              ApiKeyPhase `json:"phase,omitempty"`
	SkValue            string      `json:"sk_value,omitempty"`
	Usage              int64       `json:"usage,omitempty"`
	LastUsedAt         string      `json:"last_used_at,omitempty"`
	LastSyncAt         string      `json:"last_sync_at,omitempty"`
}

type ApiKey struct {
	ID         string        `json:"id,omitempty"`
	APIVersion string        `json:"api_version,omitempty"`
	Kind       string        `json:"kind,omitempty"`
	Metadata   *Metadata     `json:"metadata,omitempty"`
	Spec       *ApiKeySpec   `json:"spec,omitempty"`
	Status     *ApiKeyStatus `json:"status,omitempty"`
	UserID     string        `json:"user_id,omitempty"`
}

func (obj *ApiKey) GetName() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Name
}

func (obj *ApiKey) SetName(name string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Name = name
}

func (obj *ApiKey) GetWorkspace() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Workspace
}

func (obj *ApiKey) SetWorkspace(workspace string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Workspace = workspace
}

func (obj *ApiKey) GetLabels() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Labels
}

func (obj *ApiKey) SetLabels(labels map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Labels = labels
}

func (obj *ApiKey) GetAnnotations() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Annotations
}

func (obj *ApiKey) SetAnnotations(annotations map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Annotations = annotations
}

func (obj *ApiKey) GetCreationTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.CreationTimestamp
}

func (obj *ApiKey) SetCreationTimestamp(timestamp string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.CreationTimestamp = timestamp
}

func (obj *ApiKey) GetUpdateTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.UpdateTimestamp
}

func (obj *ApiKey) SetUpdateTimestamp(timestamp string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.UpdateTimestamp = timestamp
}

func (obj *ApiKey) GetDeletionTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.DeletionTimestamp
}

func (obj *ApiKey) SetDeletionTimestamp(timestamp string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.DeletionTimestamp = timestamp
}

func (obj *ApiKey) GetSpec() interface{} {
	return obj.Spec
}

func (obj *ApiKey) SetSpec(spec interface{}) {
	obj.Spec = spec.(*ApiKeySpec) //nolint:errcheck
}

func (obj *ApiKey) GetStatus() interface{} {
	return obj.Status
}

func (obj *ApiKey) SetStatus(status interface{}) {
	obj.Status = status.(*ApiKeyStatus) //nolint:errcheck
}

func (obj *ApiKey) GetKind() string {
	return obj.Kind
}

func (obj *ApiKey) SetKind(kind string) {
	obj.Kind = kind
}

func (obj *ApiKey) GetID() string {
	return obj.ID
}

func (obj *ApiKey) GetMetadata() interface{} {
	return obj.Metadata
}

func (obj *ApiKey) SetMetadata(m interface{}) {
	obj.Metadata = m.(*Metadata) //nolint:errcheck
}

// ApiKeyList is a list of ApiKey resources
type ApiKeyList struct {
	Kind  string   `json:"kind"`
	Items []ApiKey `json:"items"`
}

func (in *ApiKeyList) GetKind() string {
	return in.Kind
}

func (in *ApiKeyList) SetKind(kind string) {
	in.Kind = kind
}

func (in *ApiKeyList) GetItems() []scheme.Object {
	var objs []scheme.Object
	for i := range in.Items {
		objs = append(objs, &in.Items[i])
	}

	return objs
}

func (in *ApiKeyList) SetItems(objs []scheme.Object) {
	items := make([]ApiKey, len(objs))
	for i, obj := range objs {
		items[i] = *obj.(*ApiKey) //nolint:errcheck
	}

	in.Items = items
}
