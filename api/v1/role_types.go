package v1

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
