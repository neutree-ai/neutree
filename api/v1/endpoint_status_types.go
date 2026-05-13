package v1

// ReplicaStatus is the Demo / Phase 0 shape — flat, no nested Pools or Roles.
// Phase 1 MVP will extend with `Roles map[string]RoleStatus` per review 52587f08.
type ReplicaStatus struct {
	ID       string `json:"id"`
	NodeName string `json:"node_name,omitempty"`
	Phase    string `json:"phase,omitempty"` // Pending | Ready | Failed
}
