package v1

import (
	"strconv"

	"github.com/neutree-ai/neutree/pkg/scheme"
)

// RoleAssignmentSpec defines the desired state of RoleAssignment
type RoleAssignmentSpec struct {
	UserID    string `json:"user_id"` // UUID represented as string
	Workspace string `json:"workspace,omitempty"`
	Global    bool   `json:"global,omitempty"`
	Role      string `json:"role"`
}

// RoleAssignmentPhase represents the current phase of a RoleAssignment.
type RoleAssignmentPhase string

// RoleAssignment phase constants.
const (
	RoleAssignmentPhasePENDING RoleAssignmentPhase = "Pending"
	RoleAssignmentPhaseCREATED RoleAssignmentPhase = "Created"
	RoleAssignmentPhaseDELETED RoleAssignmentPhase = "Deleted"
)

// RoleAssignmentStatus defines the observed state of RoleAssignment
type RoleAssignmentStatus struct {
	ErrorMessage       string              `json:"error_message,omitempty"`
	LastTransitionTime string              `json:"last_transition_time,omitempty"`
	Phase              RoleAssignmentPhase `json:"phase,omitempty"`
}

// RoleAssignment is the Schema for the roleassignments API
type RoleAssignment struct {
	ID         int                   `json:"id,omitempty"`
	APIVersion string                `json:"api_version,omitempty"`
	Kind       string                `json:"kind,omitempty"`
	Metadata   *Metadata             `json:"metadata,omitempty"` // Assuming Metadata type exists
	Spec       *RoleAssignmentSpec   `json:"spec,omitempty"`
	Status     *RoleAssignmentStatus `json:"status,omitempty"`
}

func (obj *RoleAssignment) GetID() string {
	return strconv.Itoa(obj.ID)
}

func (obj *RoleAssignment) SetID(id string) {
	obj.ID, _ = strconv.Atoi(id)
}

func (obj *RoleAssignment) GetName() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Name
}

func (obj *RoleAssignment) GetWorkspace() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Workspace
}

func (obj *RoleAssignment) GetLabels() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Labels
}

func (obj *RoleAssignment) SetLabels(labels map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Labels = labels
}

func (obj *RoleAssignment) GetAnnotations() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Annotations
}

func (obj *RoleAssignment) SetAnnotations(annotations map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Annotations = annotations
}

func (obj *RoleAssignment) GetCreationTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.CreationTimestamp
}

func (obj *RoleAssignment) GetUpdateTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.UpdateTimestamp
}

func (obj *RoleAssignment) GetDeletionTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.DeletionTimestamp
}

func (obj *RoleAssignment) GetSpec() interface{} {
	return obj.Spec
}

func (obj *RoleAssignment) GetStatus() interface{} {
	return obj.Status
}

func (obj *RoleAssignment) GetKind() string {
	return obj.Kind
}

func (obj *RoleAssignment) SetKind(kind string) {
	obj.Kind = kind
}

func (obj *RoleAssignment) GetMetadata() interface{} {
	return obj.Metadata
}

// RoleAssignmentList is a list of RoleAssignment resources
type RoleAssignmentList struct {
	Kind  string           `json:"kind"`
	Items []RoleAssignment `json:"items"`
}

func (in *RoleAssignmentList) GetKind() string {
	return in.Kind
}

func (in *RoleAssignmentList) SetKind(kind string) {
	in.Kind = kind
}

func (in *RoleAssignmentList) GetItems() []scheme.Object {
	var objs []scheme.Object
	for i := range in.Items {
		objs = append(objs, &in.Items[i])
	}

	return objs
}

func (in *RoleAssignmentList) SetItems(objs []scheme.Object) {
	items := make([]RoleAssignment, len(objs))
	for i, obj := range objs {
		items[i] = *obj.(*RoleAssignment) //nolint:errcheck
	}

	in.Items = items
}
