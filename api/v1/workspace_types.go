package v1

import (
	"strconv"

	"github.com/neutree-ai/neutree/pkg/scheme"
)

type WorkspacePhase string

const (
	WorkspacePhasePENDING WorkspacePhase = "Pending"
	WorkspacePhaseCREATED WorkspacePhase = "Created"
	WorkspacePhaseDELETED WorkspacePhase = "Deleted"
)

type WorkspaceStatus struct {
	ErrorMessage       string         `json:"error_message,omitempty"`
	LastTransitionTime string         `json:"last_transition_time,omitempty"`
	Phase              WorkspacePhase `json:"phase,omitempty"`
	ServiceURL         string         `json:"service_url,omitempty"`
}

type Workspace struct {
	ID         int              `json:"id,omitempty"`
	APIVersion string           `json:"api_version,omitempty"`
	Kind       string           `json:"kind,omitempty"`
	Metadata   *Metadata        `json:"metadata,omitempty"`
	Status     *WorkspaceStatus `json:"status,omitempty"`
}

func (obj *Workspace) GetName() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Name
}

func (obj *Workspace) GetWorkspace() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Workspace
}

func (obj *Workspace) GetLabels() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Labels
}

func (obj *Workspace) SetLabels(labels map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Labels = labels
}

func (obj *Workspace) GetAnnotations() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Annotations
}

func (obj *Workspace) SetAnnotations(annotations map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Annotations = annotations
}

func (obj *Workspace) GetCreationTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.CreationTimestamp
}

func (obj *Workspace) GetUpdateTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.UpdateTimestamp
}

func (obj *Workspace) GetDeletionTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.DeletionTimestamp
}

func (obj *Workspace) GetSpec() interface{} {
	return nil
}

func (obj *Workspace) GetStatus() interface{} {
	return obj.Status
}

func (obj *Workspace) GetKind() string {
	return obj.Kind
}

func (obj *Workspace) SetKind(kind string) {
	obj.Kind = kind
}

func (obj *Workspace) GetID() string {
	return strconv.Itoa(obj.ID)
}

func (obj *Workspace) SetID(id string) {
	obj.ID, _ = strconv.Atoi(id)
}

func (obj *Workspace) GetMetadata() interface{} {
	return obj.Metadata
}

// WorkspaceList is a list of Workspace resources
type WorkspaceList struct {
	Kind  string      `json:"kind"`
	Items []Workspace `json:"items"`
}

func (in *WorkspaceList) GetKind() string {
	return in.Kind
}

func (in *WorkspaceList) SetKind(kind string) {
	in.Kind = kind
}

func (in *WorkspaceList) GetItems() []scheme.Object {
	var objs []scheme.Object
	for i := range in.Items {
		objs = append(objs, &in.Items[i])
	}

	return objs
}

func (in *WorkspaceList) SetItems(objs []scheme.Object) {
	items := make([]Workspace, len(objs))
	for i, obj := range objs {
		items[i] = *obj.(*Workspace) //nolint:errcheck
	}

	in.Items = items
}
