package strategy

import (
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// Standard is the default single-role strategy validator.
type Standard struct{}

func (s *Standard) Name() string { return "standard" }

func (s *Standard) Validate(ep *v1.Endpoint) error {
	if ep.Spec == nil {
		return fmt.Errorf("endpoint spec is nil")
	}

	if ep.Spec.Placement != nil && ep.Spec.Placement.Roles != "" && ep.Spec.Placement.Roles != "none" {
		return fmt.Errorf("standard does not allow placement.roles=%q", ep.Spec.Placement.Roles)
	}

	if len(ep.Spec.Roles) > 1 {
		return fmt.Errorf("standard requires roles to have at most one entry, got %d", len(ep.Spec.Roles))
	}

	if ep.Spec.KV != nil && ep.Spec.KV.Transfer != nil {
		return fmt.Errorf("standard does not allow kv.transfer")
	}

	return nil
}
