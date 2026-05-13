package plan

import (
	v1 "github.com/neutree-ai/neutree/api/v1"
)

// PoolFromRole converts a user-facing EndpointRoleSpec into an IR Pool.
// derivedOpts (from strategy.Compile) is strategic-merged with role.DeploymentOptions,
// with the user value taking precedence on key collisions.
func PoolFromRole(
	role v1.EndpointRoleSpec,
	instances int,
	placement *PlacementSpec,
	derivedOpts map[string]interface{},
) *Pool {
	return &Pool{
		Name:              role.Name,
		Instances:         instances,
		Resources:         role.Resources,
		Variables:         role.Variables,
		Env:               role.Env,
		DeploymentOptions: MergeOpts(derivedOpts, role.DeploymentOptions),
		Placement:         placement,
	}
}

// MakeReplicas builds n replicas via the build callback. Caller is responsible
// for setting Replica.ID (commonly "replica-{idx}").
func MakeReplicas(n int, build func(idx int) *Replica) []*Replica {
	out := make([]*Replica, n)
	for i := 0; i < n; i++ {
		out[i] = build(i)
	}
	return out
}

// MergeOpts shallow-merges derived and user maps, with user winning on collisions.
// nil inputs are tolerated.
func MergeOpts(derived, user map[string]interface{}) map[string]interface{} {
	if len(derived) == 0 && len(user) == 0 {
		return nil
	}
	out := make(map[string]interface{}, len(derived)+len(user))
	for k, v := range derived {
		out[k] = v
	}
	for k, v := range user {
		out[k] = v
	}
	return out
}
