package neutreemetrics

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/normalizer"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stretchr/testify/require"
)

func TestMetricsCollectorUsesFixedEndpointAcceleratorLabels(t *testing.T) {
	output := renderCollectorMetrics(t, []normalizer.Sample{
		{
			Name: "neutree_endpoint_replica_accelerator_utilization_ratio",
			Labels: map[string]string{
				"workspace":         "default",
				"neutree_cluster":   "k8s-a",
				"cluster_type":      "kubernetes",
				"endpoint":          "chat",
				"instance_id":       "chat-abc",
				"replica_id":        "replica-a",
				"node":              "node-a",
				"accelerator_type":  "nvidia_gpu",
				"accelerator_uuid":  "GPU-abc",
				"vdevice_index":     "0",
				"product":           "A100",
				"container":         "engine",
				"container_id":      "containerd://abc",
				"gpu_uuid":          "GPU-abc",
				"source":            "hami-monitor",
				"node_ip":           "10.0.0.10",
				"unexpected_vendor": "should-not-leak",
			},
			Value: 0.75,
		},
	})

	require.Contains(t, output, `neutree_endpoint_replica_accelerator_utilization_ratio{accelerator_index="unknown",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="kubernetes",endpoint="chat",instance_id="chat-abc",neutree_cluster="k8s-a",node="node-a",product="A100",replica_id="replica-a",vdevice_index="0",workspace="default"} 0.75`)
	require.NotContains(t, output, `container=`)
	require.NotContains(t, output, `container_id=`)
	require.NotContains(t, output, `gpu_uuid=`)
	require.NotContains(t, output, `source=`)
	require.NotContains(t, output, `node_ip=`)
	require.NotContains(t, output, `unexpected_vendor=`)
}

func TestMetricsCollectorDropsEndpointAcceleratorSamplesWithoutUUID(t *testing.T) {
	output := renderCollectorMetrics(t, []normalizer.Sample{
		{
			Name: "neutree_endpoint_replica_accelerator_utilization_ratio",
			Labels: map[string]string{
				"workspace":        "default",
				"neutree_cluster":  "k8s-a",
				"cluster_type":     "kubernetes",
				"endpoint":         "chat",
				"instance_id":      "chat-abc",
				"replica_id":       "replica-a",
				"node":             "node-a",
				"accelerator_type": "nvidia_gpu",
				"product":          "A100",
				"vdevice_index":    "0",
			},
			Value: 0.75,
		},
	})

	require.NotContains(t, output, `neutree_endpoint_replica_accelerator_utilization_ratio{`)
}

func renderCollectorMetrics(t *testing.T, samples []normalizer.Sample) string {
	t.Helper()

	registry := prometheus.NewRegistry()
	registry.MustRegister(newMetricsCollector(samples))

	request := httptest.NewRequest("GET", "/metrics", nil)
	response := httptest.NewRecorder()
	promhttp.HandlerFor(registry, promhttp.HandlerOpts{}).ServeHTTP(response, request)
	require.Equal(t, 200, response.Code)

	return strings.TrimSpace(response.Body.String())
}
