package app

import (
	"fmt"

	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

// Builder assembles the private NodeAgent application from entrypoint inputs.
type Builder struct {
	adapters []adapter.Accelerator
}

// NewBuilder creates a new NodeAgent app builder.
func NewBuilder() *Builder {
	return &Builder{
		adapters: []adapter.Accelerator{},
	}
}

func (b *Builder) WithAdapters(adapters ...adapter.Accelerator) *Builder {
	b.adapters = append(b.adapters, adapters...)
	return b
}

// Build validates the adapter set and returns the private NodeAgent app.
func (b *Builder) Build() (*App, error) {
	registry, err := newAdapterRegistry(b.adapters)
	if err != nil {
		return nil, fmt.Errorf("build accelerator adapter registry: %w", err)
	}

	if err := neutreemetrics.ValidateAdapterMetricDescriptors(registry.descriptorsCopy()); err != nil {
		return nil, fmt.Errorf("validate accelerator adapter descriptors: %w", err)
	}

	return &App{
		options:  newOptions(),
		registry: registry,
	}, nil
}
