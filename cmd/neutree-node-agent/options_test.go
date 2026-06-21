package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics"
)

func TestOptionsConfigDefaults(t *testing.T) {
	opts := newOptions()
	opts.workspace = "default"
	opts.cluster = "k8s-a"
	opts.node = "node-a"
	opts.nodeIP = "10.0.0.10"
	opts.acceleratorExporterURLs = []string{"http://127.0.0.1:9400/metrics"}

	config, err := opts.config()

	assert.NoError(t, err)
	assert.Equal(t, ":9101", config.ListenAddress)
	assert.Equal(t, "http://127.0.0.1:9100/metrics", config.NodeExporterURL)
	assert.Equal(t, []string{"http://127.0.0.1:9400/metrics"}, config.AcceleratorExporterURLs)
	assert.Equal(t, "default", config.Labels.Workspace)
	assert.Equal(t, "k8s-a", config.Labels.NeutreeCluster)
	assert.Equal(t, "kubernetes", config.Labels.ClusterType)
	assert.Equal(t, "node-a", config.Labels.Node)
	assert.Equal(t, "10.0.0.10", config.Labels.NodeIP)
	assert.Nil(t, config.KubernetesWriter)
}

func TestOptionsConfigRequiresNodeWhenKubernetesWriterEnabled(t *testing.T) {
	opts := newOptions()
	opts.enableKubernetesWriter = true

	_, err := opts.config()

	assert.ErrorContains(t, err, "node name is required")
}

func TestOptionsConfigSkipsKubernetesWriterForRay(t *testing.T) {
	opts := newOptions()
	opts.enableKubernetesWriter = true
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
	provider, ok := config.AllocationProvider.(neutreemetrics.RayServeAllocationProvider)
	require.True(t, ok)
	assert.Equal(t, "http://10.0.0.10:8265", provider.DashboardURL)
	assert.Equal(t, "head-0", provider.Node)
	assert.Equal(t, "10.0.0.10", provider.NodeIP)
}
