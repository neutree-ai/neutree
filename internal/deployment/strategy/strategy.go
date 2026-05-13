// Package strategy compiles a user-facing v1.Endpoint into the engine-agnostic
// plan.DeploymentPlan IR. Phase 0 (Demo) ships only Monolithic and a minimal
// PD same-host strategy — full PlacementProfile / Validate / NodeInfo land in
// MVP PR-03 / PR-08.
package strategy

import (
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/deployment/plan"
)

// Strategy is the role-topology template. Permanently exactly two values:
// monolithic and pd. Future parallelism axes (DP/TP/PP/EP) ride on RoleSpec.
type Strategy interface {
	Name() string
	Compile(ep *v1.Endpoint) (*plan.DeploymentPlan, error)
	Validate(ep *v1.Endpoint) error
}

// registry is a package-level singleton populated via init() hooks.
var registry = map[string]Strategy{}

// Register adds a strategy under its Name().
func Register(s Strategy) { registry[s.Name()] = s }

// Get returns the named strategy or an error.
func Get(name string) (Strategy, error) {
	s, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown strategy %q", name)
	}
	return s, nil
}
