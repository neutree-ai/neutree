package v1

// ReplicaStatus is the flat routing-domain status used by EndpointStatus.
// strategy=pd reports RoleGroup readiness here; per-role runtime details stay
// in logs, metrics, and topology surfaces.
type ReplicaStatus struct {
	ID       string `json:"id"`
	NodeName string `json:"node_name,omitempty"`
	Phase    string `json:"phase,omitempty"` // Pending | Ready | Failed
}
