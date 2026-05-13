package strategy

import (
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/deployment/plan"
)

// Monolithic is the single-role strategy. It maps the user-facing endpoint to
// N independent replicas, each with one Pool ("engine").
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

	// Single Pool per replica. Resources / engine_args inherit from top-level
	// EndpointSpec when no explicit role is set (legacy compatibility).
	role := v1.EndpointRoleSpec{
		Name:              "engine",
		Resources:         ep.Spec.Resources,
		Variables:         ep.Spec.Variables,
		Env:               ep.Spec.Env,
		DeploymentOptions: ep.Spec.DeploymentOptions,
	}
	if len(ep.Spec.Roles) == 1 {
		role = ep.Spec.Roles[0]
	}

	return &plan.DeploymentPlan{
		Replicas: plan.MakeReplicas(numReplicas, func(i int) *plan.Replica {
			return &plan.Replica{
				ID:    fmt.Sprintf("replica-%d", i),
				Pools: []*plan.Pool{plan.PoolFromRole(role, 1, nil, nil)},
			}
		}),
	}, nil
}
