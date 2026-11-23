package v1

import (
	"strconv"

	"github.com/neutree-ai/neutree/pkg/scheme"
)

// OrgPhase tells whether an OrgUnit or a Team still exists in its directory.
type OrgPhase string

const (
	OrgPhaseActive   OrgPhase = "Active"
	OrgPhaseInactive OrgPhase = "Inactive"
)

// OrgStatus is the status of an OrgUnit or a Team. A status without Phase
// keeps the stored phase; a new object starts Active.
type OrgStatus struct {
	Phase              OrgPhase `json:"phase,omitempty"`
	LastTransitionTime string   `json:"last_transition_time,omitempty"`
}

// OrgUnit is a global, sync-written department; see the org_units_and_teams
// migration for the model.
type OrgUnit struct {
	ID         int          `json:"id,omitempty"`
	APIVersion string       `json:"api_version,omitempty"`
	Kind       string       `json:"kind,omitempty"`
	Metadata   *Metadata    `json:"metadata,omitempty"`
	Spec       *OrgUnitSpec `json:"spec,omitempty"`
	Status     *OrgStatus   `json:"status,omitempty"`
	// Path is "/<root id>/.../<id>/", computed by the database from spec.parent.
	Path string `json:"path,omitempty"`
}

// OrgUnitSpec is written whole: a spec without Parent moves the OrgUnit to the root.
type OrgUnitSpec struct {
	// IdentitySource is the metadata.name of the IdentitySource the OrgUnit
	// was synced from.
	IdentitySource string `json:"identity_source"`
	// ExternalID is the directory's stable ID of the department, never a name.
	ExternalID string `json:"external_id"`
	// Parent is the metadata.name of the parent OrgUnit, from the same
	// identity source; empty for a root.
	Parent string `json:"parent,omitempty"`
}

// IsActive reports whether the OrgUnit still exists in its directory.
func (obj *OrgUnit) IsActive() bool {
	return obj.Status == nil || obj.Status.Phase != OrgPhaseInactive
}

func (obj *OrgUnit) GetName() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Name
}

func (obj *OrgUnit) GetWorkspace() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Workspace
}

func (obj *OrgUnit) GetLabels() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Labels
}

func (obj *OrgUnit) SetLabels(labels map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Labels = labels
}

func (obj *OrgUnit) GetAnnotations() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Annotations
}

func (obj *OrgUnit) SetAnnotations(annotations map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Annotations = annotations
}

func (obj *OrgUnit) GetCreationTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.CreationTimestamp
}

func (obj *OrgUnit) GetUpdateTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.UpdateTimestamp
}

func (obj *OrgUnit) GetDeletionTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.DeletionTimestamp
}

func (obj *OrgUnit) GetSpec() interface{} {
	return obj.Spec
}

func (obj *OrgUnit) GetStatus() interface{} {
	return obj.Status
}

func (obj *OrgUnit) GetKind() string {
	return obj.Kind
}

func (obj *OrgUnit) SetKind(kind string) {
	obj.Kind = kind
}

func (obj *OrgUnit) GetID() string {
	return strconv.Itoa(obj.ID)
}

func (obj *OrgUnit) SetID(id string) {
	obj.ID, _ = strconv.Atoi(id)
}

func (obj *OrgUnit) GetMetadata() interface{} {
	return obj.Metadata
}

// OrgUnitList is a list of OrgUnit resources
type OrgUnitList struct {
	Kind  string    `json:"kind"`
	Items []OrgUnit `json:"items"`
}

func (in *OrgUnitList) GetKind() string {
	return in.Kind
}

func (in *OrgUnitList) SetKind(kind string) {
	in.Kind = kind
}

func (in *OrgUnitList) GetItems() []scheme.Object {
	var objs []scheme.Object
	for i := range in.Items {
		objs = append(objs, &in.Items[i])
	}

	return objs
}

func (in *OrgUnitList) SetItems(objs []scheme.Object) {
	items := make([]OrgUnit, len(objs))
	for i, obj := range objs {
		items[i] = *obj.(*OrgUnit) //nolint:errcheck
	}

	in.Items = items
}
