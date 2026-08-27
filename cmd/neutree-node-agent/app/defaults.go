package app

import (
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/adapter/nvidia"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

// DefaultAdapters returns a fresh Community adapter slice. Enterprise
// entrypoints extend this explicit assembly input with edition-specific adapters.
func DefaultAdapters() []adapter.Accelerator {
	return []adapter.Accelerator{nvidia.NewNodeAgentAdapter()}
}
