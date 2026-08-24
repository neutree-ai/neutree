package nodeagent

import "github.com/neutree-ai/neutree/pkg/nodeagent/adapter"

// DefaultAdapters returns the reference adapters included in the community
// NodeAgent. Each call returns a fresh slice and fresh adapter instances.
func DefaultAdapters() []adapter.Accelerator {
	return []adapter.Accelerator{&nvidiaAccelerator{}}
}
