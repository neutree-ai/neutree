package v1

import "time"

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
	LastTransitionTime time.Time   `json:"last_transition_time,omitempty"`
	Phase              ApiKeyPhase `json:"phase,omitempty"`
	SkValue            string      `json:"sk_value,omitempty"`
	Usage              int64       `json:"usage,omitempty"`
	LastUsedAt         time.Time   `json:"last_used_at,omitempty"`
	LastSyncAt         time.Time   `json:"last_sync_at,omitempty"`
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
