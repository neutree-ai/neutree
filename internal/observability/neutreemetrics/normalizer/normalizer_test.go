package normalizer

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

func TestNormalizerNormalizesNodeMetrics(t *testing.T) {
	output := normalizeForTest(NormalizeRequest{
		Labels: normalizerTestLabels(),
		NodeExporter: model.ScrapeResult{
			Up: true,
			Body: `node_cpu_seconds_total{cpu="0",mode="idle"} 100
node_cpu_seconds_total{cpu="0",mode="user"} 20
node_memory_MemTotal_bytes 17179869184
node_memory_MemAvailable_bytes 6442450944
node_load1 2.5
`,
		},
	})

	assert.Contains(t, output, `neutree_metrics_scrape_up{cluster_type="ray",node="head-0",node_ip="10.0.0.10",node_role="head",source="neutree-node-agent",target="node-exporter"} 1`)
	assert.Contains(t, output, `neutree_node_ready{cluster_type="ray",node="head-0",node_ip="10.0.0.10",node_role="head",source="neutree-node-agent"} 1`)
	assert.Contains(t, output, `neutree_node_cpu_seconds_total{cluster_type="ray",cpu="0",mode="idle",node="head-0",node_ip="10.0.0.10",node_role="head",source="node-exporter"} 100`)
	assert.Contains(t, output, `neutree_node_memory_used_bytes{cluster_type="ray",node="head-0",node_ip="10.0.0.10",node_role="head",source="node-exporter"} 10737418240`)
	assert.Contains(t, output, `neutree_node_load1{cluster_type="ray",node="head-0",node_ip="10.0.0.10",node_role="head",source="node-exporter"} 2.5`)
}

func TestNormalizerPreservesAdapterSamplesAndReportsAcceleratorHealth(t *testing.T) {
	output := normalizeForTest(NormalizeRequest{
		Labels: normalizerTestLabels(),
		AcceleratorExporter: &model.ScrapeResult{
			Up:   true,
			Body: "vendor_accelerator_metric{device=\"device-a\"} 99",
		},
		AcceleratorSamples: []Sample{{
			Name:   "neutree_adapter_accelerator_metric",
			Labels: map[string]string{"accelerator_uuid": "device-a"},
			Value:  1,
		}},
	})

	assert.Contains(t, output, `neutree_metrics_scrape_up{cluster_type="ray",node="head-0",node_ip="10.0.0.10",node_role="head",source="neutree-node-agent",target="accelerator-exporter"} 1`)
	assert.Contains(t, output, `neutree_adapter_accelerator_metric{accelerator_uuid="device-a"} 1`)
	assert.NotContains(t, output, "neutree_accelerator_utilization_ratio")
}

func TestNormalizerNormalizesEndpointReplicaRuntimeUsage(t *testing.T) {
	memoryUsage := 1024.0
	workingSet := 512.0
	cpuLimit := 2.0
	memoryLimit := 2048.0
	output := normalizeForTest(NormalizeRequest{
		Labels: normalizerTestLabels(),
		EndpointReplicaRuntimeUsages: []model.EndpointReplicaRuntimeUsage{{
			Endpoint:              "chat",
			InstanceID:            "instance-a",
			ReplicaID:             "replica-a",
			NodeID:                "node-a",
			WorkloadRole:          "backend",
			Container:             "engine",
			ContainerID:           "container-a",
			Engine:                "vllm",
			EngineVersion:         "0.10.0",
			CPUUsageSeconds:       3,
			MemoryUsageBytes:      &memoryUsage,
			MemoryWorkingSetBytes: &workingSet,
			CPULimitCores:         &cpuLimit,
			MemoryLimitBytes:      &memoryLimit,
		}},
	})

	labels := `cluster_type="ray",container="engine",container_id="container-a",endpoint="chat",engine="vllm",engine_version="0.10.0",instance_id="instance-a",node="node-a",node_ip="10.0.0.10",node_role="head",replica="replica-a",source="neutree-node-agent",workload_role="backend"`
	assert.Contains(t, output, `neutree_endpoint_replica_cpu_usage_seconds_total{`+labels+`} 3`)
	assert.Contains(t, output, `neutree_endpoint_replica_memory_usage_bytes{`+labels+`} 1024`)
	assert.Contains(t, output, `neutree_endpoint_replica_memory_working_set_bytes{`+labels+`} 512`)
	assert.Contains(t, output, `neutree_endpoint_replica_cpu_limit_cores{`+labels+`} 2`)
	assert.Contains(t, output, `neutree_endpoint_replica_memory_limit_bytes{`+labels+`} 2048`)
}

func TestNormalizerUsesUnknownRuntimeRoleWhenMissing(t *testing.T) {
	output := normalizeForTest(NormalizeRequest{
		Labels: normalizerTestLabels(),
		EndpointReplicaRuntimeUsages: []model.EndpointReplicaRuntimeUsage{{
			Endpoint: "chat", CPUUsageSeconds: 1,
		}},
	})

	assert.Contains(t, output, `workload_role="unknown"`)
}

func normalizeForTest(request NormalizeRequest) string {
	var builder strings.Builder
	for _, sample := range (&Normalizer{}).Samples(request) {
		builder.WriteString(formatSample(sample))
		builder.WriteByte('\n')
	}

	return builder.String()
}

func normalizerTestLabels() adapter.CanonicalLabels {
	return adapter.CanonicalLabels{
		ClusterType: "ray",
		Node:        "head-0",
		NodeIP:      "10.0.0.10",
		NodeRole:    "head",
	}
}
