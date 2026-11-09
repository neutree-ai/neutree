package v1

// ServiceAccount is a principal of one Team for applications. It is not a
// user: it has no login and is never a member of a group. It holds no role
// assignments: its API keys act with its Team's application permissions and
// group policy.
type ServiceAccount struct {
	ID         string              `json:"id,omitempty"`
	APIVersion string              `json:"api_version,omitempty"`
	Kind       string              `json:"kind,omitempty"`
	Metadata   *Metadata           `json:"metadata,omitempty"`
	Spec       *ServiceAccountSpec `json:"spec,omitempty"`
	TeamID     int                 `json:"team_id,omitempty"`
	CreatedBy  string              `json:"created_by,omitempty"`
}

type ServiceAccountSpec struct {
	Description string `json:"description"`
	// Disabled suspends every key of the ServiceAccount.
	Disabled bool `json:"disabled"`
}

// TeamApplicationPermissions are the roles every enabled ServiceAccount of a
// Team holds, in the given workspaces. A Team without them holds the
// model-invoker role in every workspace.
type TeamApplicationPermissions struct {
	TeamID int      `json:"team_id"`
	Roles  []string `json:"roles"`
	// Workspaces where the roles are held; nil means every workspace (global).
	Workspaces      []string `json:"workspaces"`
	UpdateTimestamp string   `json:"update_timestamp,omitempty"`
}
