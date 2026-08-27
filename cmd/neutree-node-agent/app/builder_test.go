package app

import (
	"context"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

func TestNewBuilder(t *testing.T) {
	builder := NewBuilder()
	if builder == nil {
		t.Fatal("expected NewBuilder to return a non-nil Builder")
	}

	assert.Empty(t, builder.adapters)
}

func TestDefaultAdaptersReturnsFreshSlice(t *testing.T) {
	first := DefaultAdapters()
	second := DefaultAdapters()

	assert.NotNil(t, first)
	assert.NotNil(t, second)
	assert.Empty(t, first)
	assert.Empty(t, second)
}

func TestAppAddFlagsUsesCallerFlagSet(t *testing.T) {
	application, err := NewBuilder().Build()
	require.NoError(t, err)

	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	application.AddFlags(flags)

	assert.NotNil(t, flags.Lookup("listen-address"))
	assert.NotNil(t, flags.Lookup("accelerator-type"))
}

func TestBuilderBuildRejectsBuiltInDescriptorConflict(t *testing.T) {
	_, err := NewBuilder().
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

func (registryTestAdapter) BuildKubernetesMetrics(
	context.Context,
	adapter.HardwareSnapshot,
	adapter.KubernetesEvidence,
) (adapter.MetricResult, error) {
	return adapter.MetricResult{}, nil
}

func (registryTestAdapter) BuildStaticMetrics(
	context.Context,
	adapter.HardwareSnapshot,
	adapter.StaticEvidence,
) (adapter.MetricResult, error) {
	return adapter.MetricResult{}, nil
}

func (a registryTestAdapter) MetricDescriptors() []adapter.MetricDescriptor {
	return adapter.CloneMetricDescriptors(a.descriptors)
}
