package v1

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
