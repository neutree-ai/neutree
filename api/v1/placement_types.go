package v1

// PlacementSpec is the dual-axis explicit placement constraint introduced for
// PD same-host (Demo / Phase 0 minimum subset; Phase 1 adds spread-rack etc.).
//
// Replicas axis decides how independent routing-domain replicas spread across
// nodes; Roles axis decides how prefill / decode actors within one replica
// co-locate (which transitively decides the KV transfer distance).
type PlacementSpec struct {
	// Phase 0 values: "spread-node" | "pack-node" | "none". Default empty.
	Replicas string `json:"replicas,omitempty"`

	// Phase 0 values: "same-host" (PD; mandates STRICT_PACK) | "none" (monolithic).
	Roles string `json:"roles,omitempty"`
}

// EndpointRoleSpec is the per-role subspec inside EndpointSpec.Roles. Phase 0
// (Demo) keeps the minimum field set; per-role Image is intentionally not added
// (review 4a3fee48) — prefill and decode share EngineVersion.Image.
//
// Named EndpointRoleSpec (not RoleSpec) because RoleSpec is already taken by
// the RBAC role type in role_types.go.
type EndpointRoleSpec struct {
	Name              string                 `json:"name"`
	Replicas          *ReplicaSpec           `json:"replicas,omitempty"`
	Resources         *ResourceSpec          `json:"resources,omitempty"`
	Variables         map[string]interface{} `json:"variables,omitempty"`
	Env               map[string]string      `json:"env,omitempty"`
	DeploymentOptions map[string]interface{} `json:"deployment_options,omitempty"`
	Dependencies      []string               `json:"dependencies,omitempty"`
}
