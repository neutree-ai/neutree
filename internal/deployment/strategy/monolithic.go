package strategy

import (
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/deployment/plan"
)

// Monolithic is the single-role strategy. It maps the user-facing endpoint to
// NumReplicas independent routing-domain copies of one Group containing a
// single "engine" Role.
type Monolithic struct{}

func init() { Register(&Monolithic{}) }

func (s *Monolithic) Name() string { return "monolithic" }

func (s *Monolithic) Validate(ep *v1.Endpoint) error {
	if ep.Spec == nil {
		return fmt.Errorf("endpoint spec is nil")
	}
	if ep.Spec.Placement != nil && ep.Spec.Placement.Roles != "" && ep.Spec.Placement.Roles != "none" {
		return fmt.Errorf("monolithic does not allow placement.roles=%q", ep.Spec.Placement.Roles)
	}
	if len(ep.Spec.Roles) > 1 {
		return fmt.Errorf("monolithic requires roles to have at most one entry, got %d", len(ep.Spec.Roles))
	}
	return nil
}

func (s *Monolithic) Compile(ep *v1.Endpoint) (*plan.DeploymentPlan, error) {
	if err := s.Validate(ep); err != nil {
		return nil, err
	}
	numReplicas := 1
	if ep.Spec.Replicas.Num != nil && *ep.Spec.Replicas.Num > 0 {
		numReplicas = *ep.Spec.Replicas.Num
	}

	// Single Role per group. Resources / engine_args inherit from top-level
	// EndpointSpec when no explicit role is set (legacy compatibility).
	roleSpec := v1.EndpointRoleSpec{
		Name:              "engine",
		Resources:         ep.Spec.Resources,
		Variables:         ep.Spec.Variables,
		Env:               ep.Spec.Env,
		DeploymentOptions: ep.Spec.DeploymentOptions,
	}
	if len(ep.Spec.Roles) == 1 {
		roleSpec = ep.Spec.Roles[0]
	}

	// Single HTTP engine port per actor.
	engineRole := plan.RoleFromSpec(roleSpec, 1, nil)
	engineRole.PortsPerRank = 1

	return &plan.DeploymentPlan{
		NumReplicas: numReplicas,
		Group: &plan.RoleGroup{
			// Monolithic: no inter-role placement (single role auto-co-locates).
			Roles: []*plan.Role{engineRole},
		},
		// Transfer / Cache / Ports stay nil for monolithic.
	}, nil
}
