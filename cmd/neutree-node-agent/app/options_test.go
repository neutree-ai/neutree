package app

import (
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/allocation"
	metricskubernetes "github.com/neutree-ai/neutree/internal/observability/neutreemetrics/kubernetes"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

func TestOptionsConfigDefaults(t *testing.T) {
	opts := newOptions()
	opts.clusterType = v1.SSHClusterType

	config, err := opts.configWithRegistry(adapterRegistry{})

	assert.NoError(t, err)
	assert.Equal(t, ":9101", config.ListenAddress)
	assert.Equal(t, neutreemetrics.StaticScrapeTargetProvider{
		MetricsMode: neutreemetrics.MetricsModeManaged,
	}, config.ScrapeTargetProvider)
	assert.Equal(t, model.CanonicalLabels{ClusterType: v1.SSHClusterType}, config.Labels)
	assert.Nil(t, config.KubernetesWriter)
}

func TestOptionsConfigUsesExternalMetricsMode(t *testing.T) {
	opts := newOptions()
	opts.clusterType = v1.SSHClusterType
	opts.metricsMode = neutreemetrics.MetricsModeExternal

	config, err := opts.configWithRegistry(adapterRegistry{})

	require.NoError(t, err)
	assert.Equal(t, neutreemetrics.StaticScrapeTargetProvider{
		MetricsMode: neutreemetrics.MetricsModeExternal,
	}, config.ScrapeTargetProvider)
}

func TestOptionsConfigRequiresNodeForKubernetes(t *testing.T) {
	opts := newOptions()

	_, err := opts.configWithRegistry(adapterRegistry{})

	assert.ErrorContains(t, err, "node name is required")
}

func TestOptionsConfigSkipsKubernetesWriterForRay(t *testing.T) {
	opts := newOptions()
	opts.clusterType = v1.SSHClusterType

	config, err := opts.configWithRegistry(adapterRegistry{})

	assert.NoError(t, err)
	assert.Nil(t, config.KubernetesWriter)
}

func TestOptionsConfigEnablesRayAcceleratorEvidenceProvider(t *testing.T) {
	opts := newOptions()
	opts.clusterType = v1.SSHClusterType
	opts.rayDashboardURL = "http://10.0.0.10:8265"
	opts.node = "head-0"
	opts.nodeIP = "10.0.0.10"

	config, err := opts.configWithRegistry(adapterRegistry{})

	require.NoError(t, err)
	provider, ok := config.StaticAcceleratorEvidenceProvider.(allocation.RayServeAllocationProvider)
	require.True(t, ok)
	assert.Equal(t, "http://10.0.0.10:8265", provider.DashboardURL)
	assert.Equal(t, "10.0.0.10", provider.NodeIP)
	assert.Equal(t, model.CanonicalLabels{
		ClusterType: v1.SSHClusterType,
		Node:        "head-0",
		NodeIP:      "10.0.0.10",
	}, config.Labels)
}

func TestOptionsAcceleratorEvidenceProviderUsesKubernetesRawEvidence(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	writer := &metricskubernetes.AnnotationWriter{
		Client:   fake.NewClientBuilder().WithScheme(scheme).Build(),
		NodeName: "node-a",
	}
	opts := newOptions()
	opts.clusterType = v1.KubernetesClusterType

	kubernetesEvidenceProvider, staticEvidenceProvider := opts.acceleratorEvidenceProviders(writer)

	_, ok := kubernetesEvidenceProvider.(allocation.KubernetesAllocationProvider)
	assert.True(t, ok)
	assert.Nil(t, staticEvidenceProvider)
}

func TestConfigureNVIDIAKubernetesUsageBindsHAMiProviderToAdapter(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	writer := &metricskubernetes.AnnotationWriter{
		Client:   fake.NewClientBuilder().WithScheme(scheme).Build(),
		NodeName: "node-a",
	}
	registry, err := newAdapterRegistry([]adapter.Accelerator{NewNVIDIAAdapter()})
	require.NoError(t, err)

	configureNVIDIAKubernetesUsage(registry, writer)

	nvidia, ok := registry.byType[v1.AcceleratorTypeNVIDIAGPU.String()].(*nvidiaAccelerator)
	assert.True(t, ok)
	_, ok = nvidia.endpointUsageProvider.(nvidiaHAMiKubernetesUsageProvider)
	assert.True(t, ok)
}

func TestOptionsConfigAllowsUnregisteredAcceleratorType(t *testing.T) {
	opts := newOptions()
	opts.clusterType = v1.SSHClusterType
	opts.acceleratorType = "unknown-accelerator"

	config, err := opts.configWithRegistry(adapterRegistry{})

	assert.NoError(t, err)
	assert.Equal(t, "unknown-accelerator", config.AcceleratorType)
	assert.Empty(t, config.Accelerators)
}

func TestOptionsConfigAcceptsRegisteredAcceleratorType(t *testing.T) {
	registry, err := newAdapterRegistry([]adapter.Accelerator{registryTestAdapter{typ: "fixture"}})
	require.NoError(t, err)

	opts := newOptions()
	opts.clusterType = v1.SSHClusterType
	opts.acceleratorType = "fixture"

	config, err := opts.configWithRegistry(registry)

	require.NoError(t, err)
	assert.Equal(t, "fixture", config.AcceleratorType)
	assert.Contains(t, config.Accelerators, "fixture")
}

func TestOptionsConfigCarriesExplicitAcceleratorExporterTarget(t *testing.T) {
	registry, err := newAdapterRegistry([]adapter.Accelerator{registryTestAdapter{typ: "fixture"}})
	require.NoError(t, err)

	opts := newOptions()
	opts.clusterType = v1.SSHClusterType
	opts.acceleratorType = "fixture"
	opts.acceleratorExporterPort = 8082
	opts.acceleratorExporterPath = "/npu-metrics"

	config, err := opts.configWithRegistry(registry)

	require.NoError(t, err)
	assert.Equal(t, 8082, config.AcceleratorExporterPort)
	assert.Equal(t, "/npu-metrics", config.AcceleratorExporterMetricsPath)
	assert.Equal(t, neutreemetrics.StaticScrapeTargetProvider{
		MetricsMode:                    neutreemetrics.MetricsModeManaged,
		AcceleratorType:                "fixture",
		AcceleratorExporterPort:        8082,
		AcceleratorExporterMetricsPath: "/npu-metrics",
	}, config.ScrapeTargetProvider)
}

func TestOptionsConfigLeavesAdapterUnselectedWhenAcceleratorTypeEmpty(t *testing.T) {
	opts := newOptions()
	opts.clusterType = v1.SSHClusterType

	registry, err := newAdapterRegistry([]adapter.Accelerator{NewNVIDIAAdapter()})
	require.NoError(t, err)

	config, err := opts.configWithRegistry(registry)

	require.NoError(t, err)
	assert.Empty(t, config.AcceleratorType)
	assert.Contains(t, config.Accelerators, v1.AcceleratorTypeNVIDIAGPU.String())
}

func TestOptionsAcceleratorTypeFlagHelpOmitsLegacyFallbackText(t *testing.T) {
	opts := newOptions()
	flags := pflag.NewFlagSet("neutree-node-agent", pflag.ContinueOnError)

	opts.addFlags(flags)

	flag := flags.Lookup("accelerator-type")
	require.NotNil(t, flag)
	assert.NotContains(t, flag.Usage, "legacy DCGM path")
}
