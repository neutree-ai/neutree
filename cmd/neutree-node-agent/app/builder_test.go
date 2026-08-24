package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

func TestNewBuilder(t *testing.T) {
	builder := NewBuilder()
	if builder == nil {
		t.Fatal("expected NewBuilder to return a non-nil Builder")
	}

	assert.Empty(t, builder.args)
	assert.Empty(t, builder.adapters)
}

func TestBuilderWithArgsCopiesInput(t *testing.T) {
	builder := NewBuilder()
	args := []string{"--version"}

	builder.WithArgs(args)
	args[0] = "mutated"

	assert.Equal(t, []string{"--version"}, builder.args)
}

func TestBuilderBuildRejectsBuiltInDescriptorConflict(t *testing.T) {
	_, err := NewBuilder().
		WithArgs([]string{"version"}).
		WithAdapters(registryTestAdapter{
			typ: "vendor",
			descriptors: []adapter.MetricDescriptor{{
				Name: "neutree_node_ready",
			}},
		}).
		Build()

	assert.ErrorContains(t, err, `conflicts with an existing descriptor`)
}

type registryTestAdapter struct {
	typ         string
	descriptors []adapter.MetricDescriptor
}

func (a registryTestAdapter) Type() string { return a.typ }

func (registryTestAdapter) DiscoverHardware(context.Context) (adapter.HardwareSnapshot, error) {
	return adapter.HardwareSnapshot{}, nil
}

func (a registryTestAdapter) MetricDescriptors() []adapter.MetricDescriptor {
	return adapter.CloneMetricDescriptors(a.descriptors)
}
