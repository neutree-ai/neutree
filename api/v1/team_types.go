package v1

import (
	"strconv"

	"github.com/neutree-ai/neutree/pkg/scheme"
)

// Team is a flat group synced from an identity source. Teams do not nest:
// sync expands nested directory groups into direct members. Like OrgUnit, it
// is a global resource written only by directory sync, its identity
// (metadata.name, spec.identity_source, spec.external_id) never changes, and a
// group that is gone from the directory keeps its row with status.phase
// Inactive.
type Team struct {
	ID         int         `json:"id,omitempty"`
	APIVersion string      `json:"api_version,omitempty"`
	Kind       string      `json:"kind,omitempty"`
	Metadata   *Metadata   `json:"metadata,omitempty"`
	Spec       *TeamSpec   `json:"spec,omitempty"`
	Status     *TeamStatus `json:"status,omitempty"`
}

type TeamSpec struct {
	// IdentitySource is the metadata.name of the IdentitySource the Team was
	// synced from.
	IdentitySource string `json:"identity_source"`
	// ExternalID is the directory's stable ID of the group, never a name.
	ExternalID string `json:"external_id"`
}

// TeamStatus is written by sync. A status without Phase keeps the stored
// phase; a new Team starts Active.
type TeamStatus struct {
	Phase              OrgPhase `json:"phase,omitempty"`
	LastTransitionTime string   `json:"last_transition_time,omitempty"`
	ErrorMessage       string   `json:"error_message,omitempty"`
}

// IsActive reports whether the Team still exists in its directory.
func (obj *Team) IsActive() bool {
	return obj.Status == nil || obj.Status.Phase != OrgPhaseInactive
}

func (obj *Team) GetName() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Name
}

func (obj *Team) GetWorkspace() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Workspace
}

func (obj *Team) GetLabels() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Labels
}

func (obj *Team) SetLabels(labels map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Labels = labels
}

func (obj *Team) GetAnnotations() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Annotations
}

func (obj *Team) SetAnnotations(annotations map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Annotations = annotations
}

func (obj *Team) GetCreationTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.CreationTimestamp
}

func (obj *Team) GetUpdateTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.UpdateTimestamp
}

func (obj *Team) GetDeletionTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.DeletionTimestamp
}

func (obj *Team) GetSpec() interface{} {
	return obj.Spec
}

func (obj *Team) GetStatus() interface{} {
	return obj.Status
}

func (obj *Team) GetKind() string {
	return obj.Kind
}

func (obj *Team) SetKind(kind string) {
	obj.Kind = kind
}

func (obj *Team) GetID() string {
	return strconv.Itoa(obj.ID)
}

func (obj *Team) SetID(id string) {
	obj.ID, _ = strconv.Atoi(id)
}

func (obj *Team) GetMetadata() interface{} {
	return obj.Metadata
}

// TeamList is a list of Team resources
type TeamList struct {
	Kind  string `json:"kind"`
	Items []Team `json:"items"`
}

func (in *TeamList) GetKind() string {
	return in.Kind
}

func (in *TeamList) SetKind(kind string) {
	in.Kind = kind
}

func (in *TeamList) GetItems() []scheme.Object {
	var objs []scheme.Object
	for i := range in.Items {
		objs = append(objs, &in.Items[i])
	}

	return objs
}

func (in *TeamList) SetItems(objs []scheme.Object) {
	items := make([]Team, len(objs))
	for i, obj := range objs {
		items[i] = *obj.(*Team) //nolint:errcheck
	}

	in.Items = items
}
