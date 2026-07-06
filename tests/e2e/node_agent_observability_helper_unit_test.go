package e2e

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEndpointAcceleratorMetricContractAcceptsNewLabels(t *testing.T) {
	metrics := completeEndpointAcceleratorMetrics("0", "")

	require.NoError(t, validateEndpointAcceleratorMetricContract(metrics, "chat", "0"))
}

func TestEndpointAcceleratorMetricContractAllowsAnyNonEmptyVDeviceIndex(t *testing.T) {
	metrics := completeEndpointAcceleratorMetrics("2", "")

	require.NoError(t, validateEndpointAcceleratorMetricContract(metrics, "chat", ""))
}

func TestEndpointAcceleratorMetricContractRejectsOldMetricNames(t *testing.T) {
	metrics := "neutree_endpoint_replica_gpu_allocation{endpoint=\"chat\"} 1\n" +
		completeEndpointAcceleratorMetrics("0", "")

	require.Error(t, validateEndpointAcceleratorMetricContract(metrics, "chat", "0"))
}

func TestEndpointAcceleratorMetricContractRejectsExtraLabels(t *testing.T) {
	metrics := completeEndpointAcceleratorMetrics("0", `,container="engine"`)

	require.Error(t, validateEndpointAcceleratorMetricContract(metrics, "chat", "0"))
}

func completeEndpointAcceleratorMetrics(vdeviceIndex, allocationExtraLabels string) string {
	endpointLabels := fmt.Sprintf(
		`cluster_type="kubernetes",endpoint="chat",instance_id="pod-a",replica="pod-a",node="node-a",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",accelerator_index="0",vdevice_index="%s",product="NVIDIA_A100"`,
		vdeviceIndex,
	)
	allocationLabels := endpointLabels + `,vram_usage="2 GiB / 80 GiB",physical_vram_usage="42 GiB / 80 GiB"` + allocationExtraLabels
	physicalLabels := `cluster_type="kubernetes",node="node-a",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",accelerator_index="0",product="NVIDIA_A100"`
	hardwareLabels := physicalLabels + `,memory_total_bytes="85899345920",pcie_bus_id="00000000:3B:00.0",pcie_generation="4",pcie_width="16",numa_node="unknown"`
	nvidiaLabels := physicalLabels + `,architecture="Ampere",cuda_capability="8.0",driver_version="535.104.05",cuda_driver_version="12.2",nvlink="unknown",nvswitch="unknown"`

	return fmt.Sprintf(`
# HELP neutree_endpoint_replica_accelerator_allocation Neutree node-agent metric.
# TYPE neutree_endpoint_replica_accelerator_allocation gauge
neutree_endpoint_replica_accelerator_allocation{%s} 1
neutree_endpoint_replica_accelerator_utilization_ratio{%s} 0.62
neutree_node_accelerator_hardware_info{%s} 1
neutree_node_accelerator_nvidia_info{%s} 1
neutree_accelerator_pcie_tx_bytes_total{%s} 1024
neutree_accelerator_pcie_rx_bytes_total{%s} 2048
neutree_accelerator_temperature_celsius{%s} 72
`, allocationLabels, endpointLabels, hardwareLabels, nvidiaLabels, physicalLabels, physicalLabels, physicalLabels)
}
