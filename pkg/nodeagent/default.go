package nodeagent

import "github.com/neutree-ai/neutree/pkg/nodeagent/adapter"

// DefaultAdapters returns the Community NodeAgent reference adapters. Vendor
// adapters are added by their dedicated delivery domains, so the base host has
// no implicit vendor dependency.
func DefaultAdapters() []adapter.Accelerator {
	return []adapter.Accelerator{}
}
