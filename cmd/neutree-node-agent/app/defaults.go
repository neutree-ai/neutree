package app

import (
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/adapter/nvidia"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

// DefaultAdapters returns a fresh set of adapters for entrypoints that extend
// the application with edition-specific implementations.
func DefaultAdapters() []adapter.Accelerator {
	return []adapter.Accelerator{nvidia.New()}
}
