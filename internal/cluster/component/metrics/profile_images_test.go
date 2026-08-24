package metrics

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildProfileComponentImages(t *testing.T) {
	images, err := BuildProfileComponentImages("registry.example.com/neutree", v1.ClusterProfileComponents{
		NodeAgent:        v1.ImageRef{Image: "neutree/neutree-node-agent", Tag: "v1.1.0"},
		NodeExporter:     v1.ImageRef{Image: "quay.io/prometheus/node-exporter", Tag: "v1.8.2"},
		VMAgent:          v1.ImageRef{Image: "victoriametrics/vmagent", Tag: "v1.115.0"},
		KubeStateMetrics: v1.ImageRef{Image: "registry.k8s.io/kube-state-metrics/kube-state-metrics", Tag: "v2.15.0"},
	})

	require.NoError(t, err)
	assert.Equal(t, ComponentImages{
		NodeAgentImage:        "registry.example.com/neutree/neutree/neutree-node-agent:v1.1.0",
		NodeExporterImage:     "registry.example.com/neutree/prometheus/node-exporter:v1.8.2",
		VMAgentImage:          "registry.example.com/neutree/victoriametrics/vmagent:v1.115.0",
		KubeStateMetricsImage: "registry.example.com/neutree/kube-state-metrics/kube-state-metrics:v2.15.0",
	}, images)
}

func TestBuildProfileComponentImagesRejectsIncompleteReference(t *testing.T) {
	_, err := BuildProfileComponentImages("registry.example.com/neutree", v1.ClusterProfileComponents{
		NodeAgent:        v1.ImageRef{Image: "neutree/neutree-node-agent", Tag: "v1.1.0"},
		NodeExporter:     v1.ImageRef{Image: "quay.io/prometheus/node-exporter", Tag: "v1.8.2"},
		VMAgent:          v1.ImageRef{Image: "victoriametrics/vmagent"},
		KubeStateMetrics: v1.ImageRef{Image: "registry.k8s.io/kube-state-metrics/kube-state-metrics", Tag: "v2.15.0"},
	})

	require.ErrorContains(t, err, "build vmagent image from cluster profile: vmagent tag is required")
}
