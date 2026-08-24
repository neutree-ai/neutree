package nodeagent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/allocation"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/hami"
	metricskubernetes "github.com/neutree-ai/neutree/internal/observability/neutreemetrics/kubernetes"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

func TestOptionsConfigDefaults(t *testing.T) {
	opts := newOptions()
	opts.clusterType = clusterTypeRay

	config, err := opts.config()

	assert.NoError(t, err)
	assert.Equal(t, ":9101", config.ListenAddress)
	assert.Equal(t, neutreemetrics.StaticScrapeTargetProvider{
		MetricsMode: neutreemetrics.MetricsModeManaged,
	}, config.ScrapeTargetProvider)
	assert.Equal(t, model.CanonicalLabels{ClusterType: clusterTypeRay}, config.Labels)
	assert.Nil(t, config.KubernetesWriter)
}

func TestOptionsConfigUsesExternalMetricsMode(t *testing.T) {
	opts := newOptions()
	opts.clusterType = clusterTypeRay
	opts.metricsMode = neutreemetrics.MetricsModeExternal

	config, err := opts.config()

	require.NoError(t, err)
	assert.Equal(t, neutreemetrics.StaticScrapeTargetProvider{
		MetricsMode: neutreemetrics.MetricsModeExternal,
	}, config.ScrapeTargetProvider)
}

func TestOptionsConfigRequiresNodeForKubernetes(t *testing.T) {
	opts := newOptions()

	_, err := opts.config()

	assert.ErrorContains(t, err, "node name is required")
}

func TestOptionsConfigSkipsKubernetesWriterForRay(t *testing.T) {
	opts := newOptions()
	opts.clusterType = "ray"

	config, err := opts.config()

	assert.NoError(t, err)
	assert.Nil(t, config.KubernetesWriter)
}

func TestOptionsConfigEnablesRayAllocationProvider(t *testing.T) {
	opts := newOptions()
	opts.clusterType = "ray"
	opts.rayDashboardURL = "http://10.0.0.10:8265"
	opts.node = "head-0"
	opts.nodeIP = "10.0.0.10"

	config, err := opts.config()

	require.NoError(t, err)
	provider, ok := config.AllocationProvider.(allocation.RayServeAllocationProvider)
	require.True(t, ok)
	_, ok = config.StaticAcceleratorEvidenceProvider.(allocation.RayServeAllocationProvider)
	require.True(t, ok)
	assert.Equal(t, "http://10.0.0.10:8265", provider.DashboardURL)
	assert.Equal(t, "head-0", provider.Node)
	assert.Equal(t, "10.0.0.10", provider.NodeIP)
	assert.Equal(t, model.CanonicalLabels{
		ClusterType: clusterTypeRay,
		Node:        "head-0",
		NodeIP:      "10.0.0.10",
	}, config.Labels)
}

func TestOptionsAllocationProviderCombinesKubernetesAndHAMiProviders(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	writer := &metricskubernetes.AnnotationWriter{
		Client:   fake.NewClientBuilder().WithScheme(scheme).Build(),
		NodeName: "node-a",
	}
	opts := newOptions()
	opts.clusterType = clusterTypeKubernetes

	provider, kubernetesEvidenceProvider, staticEvidenceProvider := opts.allocationProvider(writer)

	multi, ok := provider.(allocation.MultiProvider)
	require.True(t, ok)
	require.Len(t, multi.Providers, 2)
	_, ok = multi.Providers[0].(allocation.KubernetesAllocationProvider)
	assert.True(t, ok)
	_, ok = multi.Providers[1].(hami.KubernetesProvider)
	assert.True(t, ok)
	_, ok = kubernetesEvidenceProvider.(allocation.KubernetesAllocationProvider)
	assert.True(t, ok)
	assert.Nil(t, staticEvidenceProvider)
}

func TestOptionsEndpointGPUUsageProviderUsesHAMiForKubernetes(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	writer := &metricskubernetes.AnnotationWriter{
		Client:   fake.NewClientBuilder().WithScheme(scheme).Build(),
		NodeName: "node-a",
	}
	opts := newOptions()
	opts.clusterType = clusterTypeKubernetes

	provider := opts.endpointGPUUsageProvider(writer)

	_, ok := provider.(hami.KubernetesProvider)
	assert.True(t, ok)
}

func TestOptionsConfigRejectsUnregisteredAcceleratorType(t *testing.T) {
	opts := newOptions()
	opts.clusterType = clusterTypeRay
	opts.acceleratorType = "unknown-accelerator"

	_, err := opts.config()

	assert.ErrorContains(t, err, "accelerator adapter \"unknown-accelerator\" is not registered")
}

func TestOptionsConfigAcceptsRegisteredAcceleratorType(t *testing.T) {
	registry, err := newAdapterRegistry([]adapter.Accelerator{registryTestAdapter{typ: "fixture"}})
	require.NoError(t, err)

	opts := newOptions()
	opts.clusterType = clusterTypeRay
	opts.acceleratorType = "fixture"

	config, err := opts.configWithRegistry(registry)

	require.NoError(t, err)
	assert.Equal(t, "fixture", config.AcceleratorType)
	assert.Contains(t, config.Accelerators, "fixture")
}

func TestOptionsConfigKeepsLegacyPathWhenAcceleratorTypeEmpty(t *testing.T) {
	opts := newOptions()
	opts.clusterType = clusterTypeRay

	config, err := opts.config()

	require.NoError(t, err)
	assert.Empty(t, config.AcceleratorType)
	assert.Empty(t, config.Accelerators)
}
