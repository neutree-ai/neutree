package app

import (
	"context"
	"errors"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
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
	require.Len(t, first, 1)
	require.Len(t, second, 1)
	assert.Equal(t, v1.AcceleratorTypeNVIDIAGPU.String(), first[0].Type())
	assert.Equal(t, v1.AcceleratorTypeNVIDIAGPU.String(), second[0].Type())
	assert.NotSame(t, first[0], second[0])
}

func TestAppAddFlagsUsesCallerFlagSet(t *testing.T) {
	application, err := NewBuilder().Build()
	require.NoError(t, err)

	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	application.AddFlags(flags)

	assert.NotNil(t, flags.Lookup("listen-address"))
	assert.NotNil(t, flags.Lookup("accelerator-type"))
}

func TestAppAddFlagsPopulatesApplicationOptions(t *testing.T) {
	application, err := NewBuilder().Build()
	require.NoError(t, err)

	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	application.AddFlags(flags)
	require.NoError(t, flags.Parse([]string{
		"--cluster-type=ssh",
		"--node-ip=10.0.0.1",
		"--accelerator-exporter-port=8082",
		"--accelerator-exporter-metrics-path=/custom-metrics",
	}))

	assert.Equal(t, "ssh", application.options.clusterType)
	assert.Equal(t, "10.0.0.1", application.options.nodeIP)
	assert.Equal(t, 8082, application.options.acceleratorExporterPort)
	assert.Equal(t, "/custom-metrics", application.options.acceleratorExporterPath)
}

func TestLegacyArgsPopulateApplicationOptions(t *testing.T) {
	application, err := NewBuilder().WithArgs([]string{
		"--cluster-type=ssh",
		"--node-ip=10.0.0.2",
		"--accelerator-exporter-port=8083",
	}).Build()
	require.NoError(t, err)

	options, err := application.runOptions()

	require.NoError(t, err)
	assert.Same(t, application.options, options)
	assert.Equal(t, "ssh", options.clusterType)
	assert.Equal(t, "10.0.0.2", options.nodeIP)
	assert.Equal(t, 8083, options.acceleratorExporterPort)
}

func TestRunOptionsRejectsMixedFlagSources(t *testing.T) {
	application, err := NewBuilder().WithArgs([]string{"--cluster-type=ssh"}).Build()
	require.NoError(t, err)

	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	application.AddFlags(flags)

	_, err = application.runOptions()

	assert.ErrorContains(t, err, "WithArgs cannot be combined with AddFlags")
}

func TestLegacyHelpReturnsPflagHelpError(t *testing.T) {
	application, err := NewBuilder().WithArgs([]string{"--help"}).Build()
	require.NoError(t, err)

	_, err = application.runOptions()

	assert.True(t, errors.Is(err, pflag.ErrHelp))
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

func TestBuilderBuildRejectsMalformedAdapterDescriptor(t *testing.T) {
	_, err := NewBuilder().
		WithAdapters(registryTestAdapter{
			typ: "vendor",
			descriptors: []adapter.MetricDescriptor{{
				Name:               "neutree_vendor_metric",
				LabelNames:         []string{"device"},
				RequiredLabelNames: []string{"missing"},
			}},
		}).
		Build()

	assert.ErrorContains(t, err, `requires unknown label "missing"`)
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
