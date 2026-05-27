package strategy

import (
	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/deployment/pdconfig"
)

// PD is the prefill / decode disaggregation strategy. Phase 1 supports
// placement.roles=same-host; the effective transfer connector is resolved from
// the selected EngineVersion's PD capabilities.
type PD struct{}

func (s *PD) Name() string { return "pd" }

// Validate enforces the API-boundary PD invariants:
//   - placement.roles must be "same-host" or empty (defaulted by strategy)
//   - roles must include both "prefill" and "decode"
func (s *PD) Validate(ep *v1.Endpoint) error {
	return pdconfig.ValidatePDSameHost(ep)
}
