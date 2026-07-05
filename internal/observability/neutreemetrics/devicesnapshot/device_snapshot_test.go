package devicesnapshot

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFromAcceleratorMetricsSetsMinorNumberFromGPUIndex(t *testing.T) {
	snapshot := FromAcceleratorMetrics(`DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-abc",modelName="A100"} 87`)

	require.NotNil(t, snapshot)
	require.Len(t, snapshot.Accelerator.Devices, 1)
	assert.Equal(t, 0, snapshot.Accelerator.Devices[0].MinorNumber)
}

func TestFromAcceleratorMetricsRequiresGPUUtilWithUUIDForDevice(t *testing.T) {
	snapshot := FromAcceleratorMetrics(`DCGM_FI_DEV_FB_TOTAL{gpu="0",UUID="GPU-abc",modelName="A100"} 81920`)

	require.NotNil(t, snapshot)
	assert.Equal(t, v1.StaticNodeAcceleratorTypeCPU, snapshot.Accelerator.Type)
	assert.Empty(t, snapshot.Accelerator.Devices)
}

func TestFromAcceleratorMetricsUsesGPUUtilGateAndEnrichesFromOtherMetrics(t *testing.T) {
	snapshot := FromAcceleratorMetrics(`DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-abc",modelName="A100"} 0
DCGM_FI_DEV_FB_TOTAL{gpu="0",UUID="GPU-abc",modelName="A100"} 81920`)

	require.NotNil(t, snapshot)
	assert.Equal(t, v1.AcceleratorTypeNVIDIAGPU.String(), snapshot.Accelerator.Type)
	require.Len(t, snapshot.Accelerator.Devices, 1)
	assert.Equal(t, "GPU-abc", snapshot.Accelerator.Devices[0].UUID)
	assert.Equal(t, "0", snapshot.Accelerator.Devices[0].ID)
	assert.Equal(t, 0, snapshot.Accelerator.Devices[0].MinorNumber)
	assert.Equal(t, "A100", snapshot.Accelerator.Devices[0].ProductName)
	assert.Equal(t, int64(81920), snapshot.Accelerator.Devices[0].MemoryMiB)
}

func TestFromAcceleratorMetricsUsesUnknownMinorNumberWhenMissing(t *testing.T) {
	snapshot := FromAcceleratorMetrics(`DCGM_FI_DEV_GPU_UTIL{UUID="GPU-abc",modelName="A100"} 87`)

	require.NotNil(t, snapshot)
	require.Len(t, snapshot.Accelerator.Devices, 1)
	assert.Equal(t, v1.StaticNodeAcceleratorDeviceMinorNumberUnknown, snapshot.Accelerator.Devices[0].MinorNumber)
}
