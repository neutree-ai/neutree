package neutreemetrics

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizerNormalizeNodeMetrics(t *testing.T) {
	output := (&Normalizer{}).Normalize(NormalizeRequest{
		Labels: testLabels(),
		NodeExporter: ScrapeResult{
			Target: TargetNodeExporter,
			Up:     true,
			Body: `# HELP node_memory_MemTotal_bytes Memory information field MemTotal_bytes.
node_memory_MemTotal_bytes 17179869184
node_memory_MemAvailable_bytes 6442450944
node_load1 2.5
`,
		},
	})

	assert.Contains(t, output, `neutree_metrics_scrape_up{cluster_type="ray",node="head-0",node_ip="10.0.0.10",node_role="head",source="neutree-metrics",static_node_cluster="static-a",target="node-exporter",workspace="default"} 1`)
	assert.Contains(t, output, `neutree_node_memory_total_bytes{cluster_type="ray",node="head-0",node_ip="10.0.0.10",node_role="head",source="node-exporter",static_node_cluster="static-a",workspace="default"} 17179869184`)
	assert.Contains(t, output, `neutree_node_memory_available_bytes{cluster_type="ray",node="head-0",node_ip="10.0.0.10",node_role="head",source="node-exporter",static_node_cluster="static-a",workspace="default"} 6442450944`)
	assert.Contains(t, output, `neutree_node_memory_used_bytes{cluster_type="ray",node="head-0",node_ip="10.0.0.10",node_role="head",source="node-exporter",static_node_cluster="static-a",workspace="default"} 10737418240`)
	assert.Contains(t, output, `neutree_node_load1{cluster_type="ray",node="head-0",node_ip="10.0.0.10",node_role="head",source="node-exporter",static_node_cluster="static-a",workspace="default"} 2.5`)
}

func TestNormalizerNormalizeNvidiaDCGMMetrics(t *testing.T) {
	output := (&Normalizer{}).Normalize(NormalizeRequest{
		Labels: testLabels(),
		NodeExporter: ScrapeResult{
			Target: TargetNodeExporter,
			Up:     false,
		},
		AcceleratorExporter: &ScrapeResult{
			Target: TargetAcceleratorExporter,
			Up:     true,
			Body: `DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-abc",device="nvidia0",modelName="A100"} 87
DCGM_FI_DEV_FB_USED{gpu="0",UUID="GPU-abc",device="nvidia0",modelName="A100"} 1024
DCGM_FI_DEV_FB_TOTAL{gpu="0",UUID="GPU-abc",device="nvidia0",modelName="A100"} 81920
`,
		},
		AcceleratorType: AcceleratorTypeNvidiaGPU,
		ExporterKind:    ExporterKindDCGM,
	})

	assert.Contains(t, output, `neutree_metrics_scrape_up{cluster_type="ray",node="head-0",node_ip="10.0.0.10",node_role="head",source="neutree-metrics",static_node_cluster="static-a",target="node-exporter",workspace="default"} 0`)
	assert.Contains(t, output, `neutree_metrics_scrape_up{cluster_type="ray",node="head-0",node_ip="10.0.0.10",node_role="head",source="neutree-metrics",static_node_cluster="static-a",target="accelerator-exporter",workspace="default"} 1`)
	assert.Contains(t, output, `neutree_gpu_utilization_ratio{cluster_type="ray",device="nvidia0",exporter_kind="dcgm-exporter",gpu="0",gpu_model="A100",gpu_uuid="GPU-abc",node="head-0",node_ip="10.0.0.10",node_role="head",source="accelerator-exporter",static_node_cluster="static-a",workspace="default"} 0.87`)
	assert.Contains(t, output, `neutree_gpu_memory_used_bytes{cluster_type="ray",device="nvidia0",exporter_kind="dcgm-exporter",gpu="0",gpu_model="A100",gpu_uuid="GPU-abc",node="head-0",node_ip="10.0.0.10",node_role="head",source="accelerator-exporter",static_node_cluster="static-a",workspace="default"} 1073741824`)
	assert.Contains(t, output, `neutree_gpu_memory_total_bytes{cluster_type="ray",device="nvidia0",exporter_kind="dcgm-exporter",gpu="0",gpu_model="A100",gpu_uuid="GPU-abc",node="head-0",node_ip="10.0.0.10",node_role="head",source="accelerator-exporter",static_node_cluster="static-a",workspace="default"} 85899345920`)
}

func TestNormalizerUnknownAcceleratorMapping(t *testing.T) {
	output := (&Normalizer{}).Normalize(NormalizeRequest{
		Labels: testLabels(),
		NodeExporter: ScrapeResult{
			Target: TargetNodeExporter,
			Up:     true,
		},
		AcceleratorExporter: &ScrapeResult{
			Target: TargetAcceleratorExporter,
			Up:     true,
			Body:   `SOME_ACCELERATOR_METRIC 1`,
		},
		AcceleratorType: "amd_gpu",
		ExporterKind:    "unknown-exporter",
	})

	assert.Contains(t, output, `neutree_metrics_mapping_supported{accelerator_type="amd_gpu",cluster_type="ray",exporter_kind="unknown-exporter",node="head-0",node_ip="10.0.0.10",node_role="head",source="neutree-metrics",static_node_cluster="static-a",workspace="default"} 0`)
	assert.NotContains(t, output, "neutree_gpu_utilization_ratio")
}

func testLabels() CanonicalLabels {
	return CanonicalLabels{
		Workspace:         "default",
		StaticNodeCluster: "static-a",
		ClusterType:       "ray",
		Node:              "head-0",
		NodeIP:            "10.0.0.10",
		NodeRole:          "head",
	}
}
