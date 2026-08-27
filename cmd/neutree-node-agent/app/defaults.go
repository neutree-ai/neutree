package app

import "github.com/neutree-ai/neutree/pkg/nodeagent/adapter"

// DefaultAdapters returns a fresh Community adapter slice. Vendor-specific
// Community and Enterprise entrypoints extend this explicit assembly input.
func DefaultAdapters() []adapter.Accelerator {
	return []adapter.Accelerator{}
}
