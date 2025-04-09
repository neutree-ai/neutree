package v1

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
