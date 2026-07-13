package v1

import (
	"github.com/neutree-ai/neutree/pkg/scheme"
)

type ApiKeySpec struct {
	Quota  int64         `json:"quota,omitempty"`
	Limits *ApiKeyLimits `json:"limits,omitempty"`
}

// ApiKeyLimits is the limit configuration carried on the API key itself. It
// holds only configuration; usage/remaining is derived from the api_daily_usage
// ledger, never stored here.
type ApiKeyLimits struct {
	TokenQuota    *ApiKeyTokenQuota `json:"token_quota,omitempty"`
	RPS           int               `json:"rps,omitempty"`
	RPM           int               `json:"rpm,omitempty"`
	Concurrency   int               `json:"concurrency,omitempty"`
	AllowedModels []AllowedModel    `json:"allowed_models,omitempty"`
	Disabled      bool              `json:"disabled,omitempty"`
}

// AllowedModel is one entry of an API key's model allowlist, scoped to the
// IE/EE (internal/external endpoint) dimension. A request is permitted when its
// client-facing model equals Model AND — for each of Type / EndpointName that is
// set — the endpoint the request actually hit matches it. Empty Type and
// EndpointName mean "any endpoint serving this model" (the legacy name-only
// semantics old keys are migrated to). A fully pinned entry (Type + EndpointName
// set) restricts to one specific endpoint, so the same model name exposed by a
// different IE/EE is not allowed.
type AllowedModel struct {
	Model string `json:"model"`
	// Type is the IE/EE token "internal" | "external" (or "" for any source).
	// Note this is NOT the DB `source` that get_workspace_models returns
	// ("endpoint" | "external_endpoint"); the UI maps source -> type when it
	// builds this entry. The gateway enforces against the same "internal" /
	// "external" tokens (see endpointTypeInternal/External in internal/gateway).
	Type         string `json:"type,omitempty"`
	EndpointName string `json:"endpoint_name,omitempty"` // "" = any endpoint of this model
}

type ApiKeyTokenQuota struct {
	Limit  int64  `json:"limit,omitempty"`
	Period string `json:"period,omitempty"`
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
	SkValue            string      `json:"sk_value,omitempty" api:"-"`
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

func (obj *ApiKey) GetWorkspace() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Workspace
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

func (obj *ApiKey) GetUpdateTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.UpdateTimestamp
}

func (obj *ApiKey) GetDeletionTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.DeletionTimestamp
}

func (obj *ApiKey) GetSpec() interface{} {
	return obj.Spec
}

func (obj *ApiKey) GetStatus() interface{} {
	return obj.Status
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
