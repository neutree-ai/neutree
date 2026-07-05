package e2e

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEndpointAcceleratorMetricContractAcceptsNewLabels(t *testing.T) {
	metrics := `
# HELP neutree_endpoint_replica_accelerator_allocation Neutree node-agent metric.
# TYPE neutree_endpoint_replica_accelerator_allocation gauge
neutree_endpoint_replica_accelerator_allocation{workspace="default",neutree_cluster="k8s-a",cluster_type="kubernetes",endpoint="chat",instance_id="pod-a",replica_id="pod-a",node="node-a",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",accelerator_index="0",vdevice_index="0",product="NVIDIA_A100"} 1
neutree_endpoint_replica_accelerator_memory_allocated_bytes{workspace="default",neutree_cluster="k8s-a",cluster_type="kubernetes",endpoint="chat",instance_id="pod-a",replica_id="pod-a",node="node-a",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",accelerator_index="0",vdevice_index="0",product="NVIDIA_A100"} 85899345920
neutree_endpoint_replica_accelerator_memory_used_bytes{workspace="default",neutree_cluster="k8s-a",cluster_type="kubernetes",endpoint="chat",instance_id="pod-a",replica_id="pod-a",node="node-a",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",accelerator_index="0",vdevice_index="0",product="NVIDIA_A100"} 2147483648
neutree_endpoint_replica_accelerator_utilization_ratio{workspace="default",neutree_cluster="k8s-a",cluster_type="kubernetes",endpoint="chat",instance_id="pod-a",replica_id="pod-a",node="node-a",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",accelerator_index="0",vdevice_index="0",product="NVIDIA_A100"} 0.62
`

	require.NoError(t, validateEndpointAcceleratorMetricContract(metrics, "chat", "0"))
}

func TestEndpointAcceleratorMetricContractAllowsAnyNonEmptyVDeviceIndex(t *testing.T) {
	metrics := `
neutree_endpoint_replica_accelerator_allocation{workspace="default",neutree_cluster="k8s-a",cluster_type="kubernetes",endpoint="chat",instance_id="pod-a",replica_id="pod-a",node="node-a",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",accelerator_index="0",vdevice_index="2",product="NVIDIA_A100"} 1
neutree_endpoint_replica_accelerator_memory_allocated_bytes{workspace="default",neutree_cluster="k8s-a",cluster_type="kubernetes",endpoint="chat",instance_id="pod-a",replica_id="pod-a",node="node-a",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",accelerator_index="0",vdevice_index="2",product="NVIDIA_A100"} 85899345920
neutree_endpoint_replica_accelerator_memory_used_bytes{workspace="default",neutree_cluster="k8s-a",cluster_type="kubernetes",endpoint="chat",instance_id="pod-a",replica_id="pod-a",node="node-a",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",accelerator_index="0",vdevice_index="2",product="NVIDIA_A100"} 2147483648
neutree_endpoint_replica_accelerator_utilization_ratio{workspace="default",neutree_cluster="k8s-a",cluster_type="kubernetes",endpoint="chat",instance_id="pod-a",replica_id="pod-a",node="node-a",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",accelerator_index="0",vdevice_index="2",product="NVIDIA_A100"} 0.62
`

	require.NoError(t, validateEndpointAcceleratorMetricContract(metrics, "chat", ""))
}

func TestEndpointAcceleratorMetricContractRejectsOldMetricNames(t *testing.T) {
	metrics := `
neutree_endpoint_replica_gpu_allocation{endpoint="chat"} 1
neutree_endpoint_replica_accelerator_allocation{workspace="default",neutree_cluster="k8s-a",cluster_type="kubernetes",endpoint="chat",instance_id="pod-a",replica_id="pod-a",node="node-a",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",accelerator_index="0",vdevice_index="0",product="NVIDIA_A100"} 1
neutree_endpoint_replica_accelerator_memory_allocated_bytes{workspace="default",neutree_cluster="k8s-a",cluster_type="kubernetes",endpoint="chat",instance_id="pod-a",replica_id="pod-a",node="node-a",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",accelerator_index="0",vdevice_index="0",product="NVIDIA_A100"} 85899345920
neutree_endpoint_replica_accelerator_memory_used_bytes{workspace="default",neutree_cluster="k8s-a",cluster_type="kubernetes",endpoint="chat",instance_id="pod-a",replica_id="pod-a",node="node-a",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",accelerator_index="0",vdevice_index="0",product="NVIDIA_A100"} 2147483648
neutree_endpoint_replica_accelerator_utilization_ratio{workspace="default",neutree_cluster="k8s-a",cluster_type="kubernetes",endpoint="chat",instance_id="pod-a",replica_id="pod-a",node="node-a",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",accelerator_index="0",vdevice_index="0",product="NVIDIA_A100"} 0.62
`

	require.Error(t, validateEndpointAcceleratorMetricContract(metrics, "chat", "0"))
}

func TestEndpointAcceleratorMetricContractRejectsExtraLabels(t *testing.T) {
	metrics := `
neutree_endpoint_replica_accelerator_allocation{workspace="default",neutree_cluster="k8s-a",cluster_type="kubernetes",endpoint="chat",instance_id="pod-a",replica_id="pod-a",node="node-a",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",accelerator_index="0",vdevice_index="0",product="NVIDIA_A100",container="engine"} 1
neutree_endpoint_replica_accelerator_memory_allocated_bytes{workspace="default",neutree_cluster="k8s-a",cluster_type="kubernetes",endpoint="chat",instance_id="pod-a",replica_id="pod-a",node="node-a",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",accelerator_index="0",vdevice_index="0",product="NVIDIA_A100",container="engine"} 85899345920
neutree_endpoint_replica_accelerator_memory_used_bytes{workspace="default",neutree_cluster="k8s-a",cluster_type="kubernetes",endpoint="chat",instance_id="pod-a",replica_id="pod-a",node="node-a",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",accelerator_index="0",vdevice_index="0",product="NVIDIA_A100",container="engine"} 2147483648
neutree_endpoint_replica_accelerator_utilization_ratio{workspace="default",neutree_cluster="k8s-a",cluster_type="kubernetes",endpoint="chat",instance_id="pod-a",replica_id="pod-a",node="node-a",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",accelerator_index="0",vdevice_index="0",product="NVIDIA_A100",container="engine"} 0.62
`

	require.Error(t, validateEndpointAcceleratorMetricContract(metrics, "chat", "0"))
}
