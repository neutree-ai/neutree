package v1

// PlacementSpec is the dual-axis explicit placement constraint for PD
// same-host.
//
// Replicas axis decides how independent routing-domain replicas spread across
// nodes; Roles axis decides how prefill / decode actors within one replica
// co-locate (which transitively decides the KV transfer distance).
type PlacementSpec struct {
	// Values: "spread-node" | "pack-node" | "none". Empty defaults by strategy.
	Replicas string `json:"replicas,omitempty"`

	// Values: "same-host" (PD; mandates role co-location) | "none".
	Roles string `json:"roles,omitempty"`
}

// EndpointRoleSpec is the per-role subspec inside EndpointSpec.Roles. Per-role
// image is intentionally not added: prefill and decode share EngineVersion.Image.
//
// Named EndpointRoleSpec (not RoleSpec) because RoleSpec is already taken by
// the RBAC role type in role_types.go.
type EndpointRoleSpec struct {
	Name      string                 `json:"name"`
	Replicas  *ReplicaSpec           `json:"replicas,omitempty"`
	Resources *ResourceSpec          `json:"resources,omitempty"`
	Variables map[string]interface{} `json:"variables,omitempty"`
	Env       map[string]string      `json:"env,omitempty"`
}
