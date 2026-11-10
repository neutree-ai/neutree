package v1

import (
	"strconv"

	"github.com/neutree-ai/neutree/pkg/scheme"
)

// GroupPolicy holds the model access rules of one group (a Team or an
// OrgUnit), or of one user or ServiceAccount in one scope. A group has at most
// one; a user or ServiceAccount at most one per scope. Group rules bind every
// member, also while the group is Inactive; a subject policy binds its
// subject while the subject is in its scope.
type GroupPolicy struct {
	ID         int              `json:"id,omitempty"`
	APIVersion string           `json:"api_version,omitempty"`
	Kind       string           `json:"kind,omitempty"`
	Metadata   *Metadata        `json:"metadata,omitempty"`
	Spec       *GroupPolicySpec `json:"spec,omitempty"`
}

const (
	GroupPolicySubjectTeam    = "Team"
	GroupPolicySubjectOrgUnit = "OrgUnit"
	// SubjectName is the user's id.
	GroupPolicySubjectUser = "User"
	// SubjectName is the ServiceAccount's id.
	GroupPolicySubjectServiceAccount = "ServiceAccount"
)

// GroupPolicySpec is replaced as a whole on PATCH: send every field.
type GroupPolicySpec struct {
	// SubjectKind is Team, OrgUnit, User or ServiceAccount; SubjectName the
	// group's metadata.name, or the id of the user or ServiceAccount. Neither
	// changes after creation.
	SubjectKind string `json:"subject_kind,omitempty"`
	SubjectName string `json:"subject_name,omitempty"`
	// ScopeKind (Team or OrgUnit) and ScopeName name who manages a User or
	// ServiceAccount policy; empty for a global one and for group policies.
	// Neither changes after creation.
	ScopeKind string `json:"scope_kind,omitempty"`
	ScopeName string `json:"scope_name,omitempty"`
	// Empty means no rule.
	AllowedModels []GroupPolicyModel `json:"allowed_models,omitempty"`
	DeniedModels  []GroupPolicyModel `json:"denied_models,omitempty"`
	// TokenQuota is one pool shared by all members (for a User or
	// ServiceAccount, all its keys); nil means no pool.
	TokenQuota *GroupTokenQuota `json:"token_quota,omitempty"`
}

// GroupTokenQuota limits the tokens all members use per period (daily,
// weekly, monthly or yearly; monthly when empty), as ApiKeyTokenQuota does
// for one key.
type GroupTokenQuota struct {
	Limit  int64  `json:"limit"`
	Period string `json:"period,omitempty"`
}

// GroupPolicyModel names a client-facing model, in one workspace or (empty
// Workspace) in all.
type GroupPolicyModel struct {
	Model     string `json:"model"`
	Workspace string `json:"workspace,omitempty"`
}

func (obj *GroupPolicy) GetName() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Name
}

func (obj *GroupPolicy) GetWorkspace() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Workspace
}

func (obj *GroupPolicy) GetLabels() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Labels
}

func (obj *GroupPolicy) SetLabels(labels map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Labels = labels
}

func (obj *GroupPolicy) GetAnnotations() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Annotations
}

func (obj *GroupPolicy) SetAnnotations(annotations map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Annotations = annotations
}

func (obj *GroupPolicy) GetCreationTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.CreationTimestamp
}

func (obj *GroupPolicy) GetUpdateTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.UpdateTimestamp
}

func (obj *GroupPolicy) GetDeletionTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.DeletionTimestamp
}

func (obj *GroupPolicy) GetSpec() interface{} {
	return obj.Spec
}

func (obj *GroupPolicy) GetStatus() interface{} {
	return nil
}

func (obj *GroupPolicy) GetKind() string {
	return obj.Kind
}

func (obj *GroupPolicy) SetKind(kind string) {
	obj.Kind = kind
}

func (obj *GroupPolicy) GetID() string {
	return strconv.Itoa(obj.ID)
}

func (obj *GroupPolicy) SetID(id string) {
	obj.ID, _ = strconv.Atoi(id)
}

func (obj *GroupPolicy) GetMetadata() interface{} {
	return obj.Metadata
}

// GroupPolicyList is a list of GroupPolicy resources
type GroupPolicyList struct {
	Kind  string        `json:"kind"`
	Items []GroupPolicy `json:"items"`
}

func (in *GroupPolicyList) GetKind() string {
	return in.Kind
}

func (in *GroupPolicyList) SetKind(kind string) {
	in.Kind = kind
}

func (in *GroupPolicyList) GetItems() []scheme.Object {
	var objs []scheme.Object
	for i := range in.Items {
		objs = append(objs, &in.Items[i])
	}

	return objs
}

func (in *GroupPolicyList) SetItems(objs []scheme.Object) {
	items := make([]GroupPolicy, len(objs))
	for i, obj := range objs {
		items[i] = *obj.(*GroupPolicy) //nolint:errcheck
	}

	in.Items = items
}
