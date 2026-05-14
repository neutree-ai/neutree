package plan

import (
	v1 "github.com/neutree-ai/neutree/api/v1"
)

// RoleFromSpec converts a user-facing EndpointRoleSpec into an IR Role.
// derivedOpts (from strategy.Compile) is strategic-merged with
// spec.DeploymentOptions, with the user value taking precedence on key
// collisions.
func RoleFromSpec(
	spec v1.EndpointRoleSpec,
	instances int,
	derivedOpts map[string]interface{},
) *Role {
	return &Role{
		Name:              spec.Name,
		Instances:         instances,
		Resources:         spec.Resources,
		Variables:         spec.Variables,
		Env:               spec.Env,
		DeploymentOptions: MergeOpts(derivedOpts, spec.DeploymentOptions),
	}
}

// MergeOpts shallow-merges derived and user maps, with user winning on
// collisions. nil inputs are tolerated.
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
