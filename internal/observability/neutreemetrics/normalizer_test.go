package neutreemetrics

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	assert.Contains(t, output, `neutree_metrics_scrape_up{cluster_type="ray",node="head-0",node_ip="10.0.0.10",node_role="head",source="neutree-node-agent",static_node_cluster="static-a",target="node-exporter",workspace="default"} 1`)
	assert.Contains(t, output, `neutree_node_ready{cluster_type="ray",neutree_cluster="static-a",node="head-0",node_ip="10.0.0.10",node_role="head",source="neutree-node-agent",static_node_cluster="static-a",workspace="default"} 1`)
	assert.Contains(t, output, `neutree_node_memory_total_bytes{cluster_type="ray",node="head-0",node_ip="10.0.0.10",node_role="head",source="node-exporter",static_node_cluster="static-a",workspace="default"} 17179869184`)
	assert.Contains(t, output, `neutree_node_memory_available_bytes{cluster_type="ray",node="head-0",node_ip="10.0.0.10",node_role="head",source="node-exporter",static_node_cluster="static-a",workspace="default"} 6442450944`)
	assert.Contains(t, output, `neutree_node_memory_used_bytes{cluster_type="ray",node="head-0",node_ip="10.0.0.10",node_role="head",source="node-exporter",static_node_cluster="static-a",workspace="default"} 10737418240`)
	assert.Contains(t, output, `neutree_node_load1{cluster_type="ray",node="head-0",node_ip="10.0.0.10",node_role="head",source="node-exporter",static_node_cluster="static-a",workspace="default"} 2.5`)
}

func TestNormalizerNormalizesAcceleratorExporterAndEndpointAllocations(t *testing.T) {
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
		EndpointAllocations: []EndpointAllocation{
			{
				Workspace:  "default",
				Cluster:    "static-a",
				Endpoint:   "chat",
				InstanceID: "chat-replica-a",
				ReplicaID:  "replica-a",
				NodeID:     "head-0",
				Devices: []v1.DeviceAllocation{
					{
						UUID:      "GPU-abc",
						Product:   "NVIDIA_A100",
						MemoryMiB: 81920,
						CoreUnits: 100,
						NodeID:    "head-0",
					},
				},
			},
		},
	})

	assert.Contains(t, output, `neutree_metrics_scrape_up{cluster_type="ray",node="head-0",node_ip="10.0.0.10",node_role="head",source="neutree-node-agent",static_node_cluster="static-a",target="node-exporter",workspace="default"} 0`)
	assert.Contains(t, output, `neutree_metrics_scrape_up{cluster_type="ray",node="head-0",node_ip="10.0.0.10",node_role="head",source="neutree-node-agent",static_node_cluster="static-a",target="accelerator-exporter",workspace="default"} 1`)
	assert.Contains(t, output, `neutree_gpu_utilization_ratio{cluster_type="ray",gpu_index="0",gpu_uuid="GPU-abc",model="A100",neutree_cluster="static-a",node="head-0",node_ip="10.0.0.10",node_role="head",source="accelerator-exporter",static_node_cluster="static-a",workspace="default"} 0.87`)
	assert.Contains(t, output, `neutree_gpu_memory_used_bytes{cluster_type="ray",gpu_index="0",gpu_uuid="GPU-abc",model="A100",neutree_cluster="static-a",node="head-0",node_ip="10.0.0.10",node_role="head",source="accelerator-exporter",static_node_cluster="static-a",workspace="default"} 1073741824`)
	assert.Contains(t, output, `neutree_gpu_memory_total_bytes{cluster_type="ray",gpu_index="0",gpu_uuid="GPU-abc",model="A100",neutree_cluster="static-a",node="head-0",node_ip="10.0.0.10",node_role="head",source="accelerator-exporter",static_node_cluster="static-a",workspace="default"} 85899345920`)
	assert.Contains(t, output, `neutree_node_gpu_total{accelerator_type="nvidia_gpu",cluster_type="ray",neutree_cluster="static-a",node="head-0",node_ip="10.0.0.10",node_role="head",product="A100",source="neutree-node-agent",static_node_cluster="static-a",workspace="default"} 1`)
	assert.Contains(t, output, `neutree_node_gpu_allocated{accelerator_type="nvidia_gpu",cluster_type="ray",neutree_cluster="static-a",node="head-0",node_ip="10.0.0.10",node_role="head",product="A100",source="neutree-node-agent",static_node_cluster="static-a",workspace="default"} 1`)
	assert.Contains(t, output, `neutree_node_gpu_free{accelerator_type="nvidia_gpu",cluster_type="ray",neutree_cluster="static-a",node="head-0",node_ip="10.0.0.10",node_role="head",product="A100",source="neutree-node-agent",static_node_cluster="static-a",workspace="default"} 0`)
	assert.Contains(t, output, `neutree_node_gpu_info{accelerator_type="nvidia_gpu",cluster_type="ray",gpu_index="0",gpu_uuid="GPU-abc",neutree_cluster="static-a",node="head-0",node_ip="10.0.0.10",node_role="head",product="A100",source="neutree-node-agent",static_node_cluster="static-a",workspace="default"} 1`)
	assert.Contains(t, output, `neutree_endpoint_replica_gpu_allocation{cluster_type="ray",endpoint="chat",gpu_uuid="GPU-abc",instance_id="chat-replica-a",neutree_cluster="static-a",node="head-0",node_ip="10.0.0.10",node_role="head",product="NVIDIA_A100",replica_id="replica-a",source="neutree-node-agent",static_node_cluster="static-a",workspace="default"} 1`)
	assert.Contains(t, output, `neutree_node_gpu_allocation{cluster_type="ray",endpoint="chat",gpu_uuid="GPU-abc",instance_id="chat-replica-a",neutree_cluster="static-a",node="head-0",node_ip="10.0.0.10",node_role="head",product="NVIDIA_A100",replica="replica-a",replica_id="replica-a",source="neutree-node-agent",static_node_cluster="static-a",workspace="default"} 1`)
	assert.NotContains(t, output, "neutree_metrics_mapping_supported")
}

func TestNormalizerParsesDCGMLabelsWithSpaces(t *testing.T) {
	raw := `DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-abc",device="nvidia0",modelName="Tesla T4",Hostname="gpu-node"} 87
DCGM_FI_DEV_FB_TOTAL{gpu="0",UUID="GPU-abc",device="nvidia0",modelName="Tesla T4",Hostname="gpu-node"} 15360
`

	output := (&Normalizer{}).Normalize(NormalizeRequest{
		Labels: testLabels(),
		AcceleratorExporter: &ScrapeResult{
			Target: TargetAcceleratorExporter,
			Up:     true,
			Body:   raw,
		},
	})

	assert.Contains(t, output, `neutree_gpu_utilization_ratio{cluster_type="ray",gpu_index="0",gpu_uuid="GPU-abc",model="Tesla T4",neutree_cluster="static-a",node="head-0",node_ip="10.0.0.10",node_role="head",source="accelerator-exporter",static_node_cluster="static-a",workspace="default"} 0.87`)
	assert.Contains(t, output, `neutree_gpu_memory_total_bytes{cluster_type="ray",gpu_index="0",gpu_uuid="GPU-abc",model="Tesla T4",neutree_cluster="static-a",node="head-0",node_ip="10.0.0.10",node_role="head",source="accelerator-exporter",static_node_cluster="static-a",workspace="default"} 16106127360`)

	snapshot := snapshotFromAcceleratorMetrics(raw)
	require.NotNil(t, snapshot)
	assert.Equal(t, v1.AcceleratorTypeNVIDIAGPU.String(), snapshot.Accelerator.Type)
	require.Len(t, snapshot.Accelerator.Devices, 1)
	assert.Equal(t, "GPU-abc", snapshot.Accelerator.Devices[0].UUID)
	assert.Equal(t, "Tesla T4", snapshot.Accelerator.Devices[0].ProductName)
	assert.Equal(t, int64(15360), snapshot.Accelerator.Devices[0].MemoryMiB)
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
