package v1

import (
	"strconv"

	"github.com/neutree-ai/neutree/pkg/scheme"
)

type RolePreset string

type RoleSpec struct {
	PresetKey   *RolePreset `json:"preset_key,omitempty"`
	Permissions []string    `json:"permissions"`
}

type RolePhase string

const (
	RolePhasePENDING RolePhase = "Pending"
	RolePhaseCREATED RolePhase = "Created"
	RolePhaseDELETED RolePhase = "Deleted"
)

type RoleStatus struct {
	ErrorMessage       string    `json:"error_message,omitempty"`
	LastTransitionTime string    `json:"last_transition_time,omitempty"`
	Phase              RolePhase `json:"phase,omitempty"`
}

type Role struct {
	ID         int         `json:"id,omitempty"`
	APIVersion string      `json:"api_version,omitempty"`
	Kind       string      `json:"kind,omitempty"`
	Metadata   *Metadata   `json:"metadata,omitempty"`
	Spec       *RoleSpec   `json:"spec,omitempty"`
	Status     *RoleStatus `json:"status,omitempty"`
}

func (obj *Role) GetName() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Name
}

func (obj *Role) GetWorkspace() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Workspace
}

func (obj *Role) GetLabels() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Labels
}

func (obj *Role) SetLabels(labels map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Labels = labels
}

func (obj *Role) GetAnnotations() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Annotations
}

func (obj *Role) SetAnnotations(annotations map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Annotations = annotations
}

func (obj *Role) GetCreationTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.CreationTimestamp
}

func (obj *Role) GetUpdateTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.UpdateTimestamp
}

func (obj *Role) GetDeletionTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.DeletionTimestamp
}

func (obj *Role) GetSpec() interface{} {
	return obj.Spec
}

func (obj *Role) GetStatus() interface{} {
	return obj.Status
}

func (obj *Role) GetKind() string {
	return obj.Kind
}

func (obj *Role) SetKind(kind string) {
	obj.Kind = kind
}

func (obj *Role) GetID() string {
	return strconv.Itoa(obj.ID)
}

func (obj *Role) SetID(id string) {
	obj.ID, _ = strconv.Atoi(id)
}

func (obj *Role) GetMetadata() interface{} {
	return obj.Metadata
}

// RoleList is a list of Role resources
type RoleList struct {
	Kind  string `json:"kind"`
	Items []Role `json:"items"`
}

func (in *RoleList) GetKind() string {
	return in.Kind
}

func (in *RoleList) SetKind(kind string) {
	in.Kind = kind
}

func (in *RoleList) GetItems() []scheme.Object {
	var objs []scheme.Object
	for i := range in.Items {
		objs = append(objs, &in.Items[i])
	}

	return objs
}

func (in *RoleList) SetItems(objs []scheme.Object) {
	items := make([]Role, len(objs))
	for i, obj := range objs {
		items[i] = *obj.(*Role) //nolint:errcheck
	}

	in.Items = items
}
