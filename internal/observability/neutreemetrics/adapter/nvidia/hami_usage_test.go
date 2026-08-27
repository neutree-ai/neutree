package nvidia

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNvidiaHAMiEndpointReplicaUsagesFromMetrics(t *testing.T) {
	raw := `
hami_vgpu_memory_limit_bytes{namespace="default",pod="chat-abc",container="engine",device_uuid="GPU-abc",vdevice_index="0",node="node-a",device_name="NVIDIA_A100"} 8589934592
hami_vgpu_memory_used_bytes{namespace="default",pod="chat-abc",container="engine",device_uuid="GPU-abc",vdevice_index="0",node="node-a",device_name="NVIDIA_A100"} 4294967296
hami_container_device_utilization_ratio{namespace="default",pod="chat-abc",container="engine",device_uuid="GPU-abc",vdevice_index="0",node="node-a",device_name="NVIDIA_A100"} 0.75
hami_vgpu_memory_used_bytes{namespace="default",pod="sidecar",container="debug",device_uuid="GPU-ignored",vdevice_index="0",node="node-a"} 1024
`
	pods := map[podKey]podIdentity{
		{namespace: "default", name: "chat-abc"}: {
			workspace: "team-a",
			cluster:   "k8s-a",
			endpoint:  "chat",
			node:      "node-a",
		},
	}

	usages := nvidiaHAMiEndpointReplicaUsagesFromMetrics(raw, pods)

	require.Len(t, usages, 1)
	assert.Equal(t, "team-a", usages[0].Workspace)
	assert.Equal(t, "k8s-a", usages[0].Cluster)
	assert.Equal(t, "chat", usages[0].Endpoint)
	assert.Equal(t, "chat-abc", usages[0].InstanceID)
	assert.Equal(t, "node-a", usages[0].NodeID)
	assert.Equal(t, "GPU-abc", usages[0].GPUUUID)
	assert.Equal(t, "0", usages[0].VDeviceIndex)
	require.NotNil(t, usages[0].MemoryUsedBytes)
	assert.Equal(t, 4294967296.0, *usages[0].MemoryUsedBytes)
	require.NotNil(t, usages[0].UtilizationRatio)
	assert.Equal(t, 0.75, *usages[0].UtilizationRatio)
}

func TestNvidiaHAMiEndpointReplicaUsagesAggregatesUsageAndNormalizesPercentages(t *testing.T) {
	raw := `
hami_vgpu_memory_used_bytes{namespace="default",pod="chat-abc",container="engine",device_uuid="GPU-abc",vdevice_index="0"} 100
hami_vgpu_memory_used_bytes{namespace="default",pod="chat-abc",container="engine",device_uuid="GPU-abc",vdevice_index="0"} 200
hami_container_device_utilization_ratio{namespace="default",pod="chat-abc",container="engine",device_uuid="GPU-abc",vdevice_index="0"} 75
hami_container_device_utilization_ratio{namespace="default",pod="chat-abc",container="engine",device_uuid="GPU-abc",vdevice_index="0"} 0.5
hami_vgpu_memory_used_bytes{namespace="default",pod="chat-abc",container="engine"} 300
`
	pods := map[podKey]podIdentity{
		{namespace: "default", name: "chat-abc"}: {endpoint: "chat"},
	}

	usages := nvidiaHAMiEndpointReplicaUsagesFromMetrics(raw, pods)

	require.Len(t, usages, 1)
	require.NotNil(t, usages[0].MemoryUsedBytes)
	assert.Equal(t, 300.0, *usages[0].MemoryUsedBytes)
	require.NotNil(t, usages[0].UtilizationRatio)
	assert.Equal(t, 0.75, *usages[0].UtilizationRatio)
}
