package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/allocation"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/hami"
	metricskubernetes "github.com/neutree-ai/neutree/internal/observability/neutreemetrics/kubernetes"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
)

func TestOptionsConfigDefaults(t *testing.T) {
	opts := newOptions()
	opts.clusterType = clusterTypeRay
	opts.acceleratorExporterURLs = []string{"http://127.0.0.1:9400/metrics"}

	config, err := opts.config()

	assert.NoError(t, err)
	assert.Equal(t, ":9101", config.ListenAddress)
	assert.Equal(t, "http://127.0.0.1:9100/metrics", config.NodeExporterURL)
	assert.Equal(t, []string{"http://127.0.0.1:9400/metrics"}, config.AcceleratorExporterURLs)
	assert.Equal(t, model.CanonicalLabels{ClusterType: clusterTypeRay}, config.Labels)
	assert.Nil(t, config.KubernetesWriter)
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

	provider := opts.allocationProvider(writer)

	multi, ok := provider.(allocation.MultiProvider)
	require.True(t, ok)
	require.Len(t, multi.Providers, 2)
	_, ok = multi.Providers[0].(allocation.KubernetesAllocationProvider)
	assert.True(t, ok)
	_, ok = multi.Providers[1].(hami.KubernetesProvider)
	assert.True(t, ok)
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
