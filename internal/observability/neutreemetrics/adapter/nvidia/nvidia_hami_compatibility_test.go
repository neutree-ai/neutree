package nvidia

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

func TestNvidiaHAMiV210AllocationAnnotations(t *testing.T) {
	const (
		firstGPUUUID  = "GPU-12345678-1234-1234-1234-123456789012"
		secondGPUUUID = "GPU-12345678-1234-1234-1234-123456789013"
		product       = "NVIDIA-Tesla T4"
	)
	nodeAnnotations := map[string]string{
		"hami.io/node-nvidia-register": fmt.Sprintf(`[
{"id":"%s","count":10,"devmem":15360,"devcore":100,"type":"NVIDIA-Tesla T4","health":true,"mode":"hami-core"},
{"id":"%s","index":1,"count":10,"devmem":15360,"devcore":100,"type":"NVIDIA-Tesla T4","health":true,"mode":"hami-core"}
]`, firstGPUUUID, secondGPUUUID),
	}
	cases := []struct {
		name       string
		annotation string
		devices    []v1.DeviceAllocation
	}{
		{
			name:       "device_indices_survive_uuid_sorting",
			annotation: secondGPUUUID + ",NVIDIA,8192,50:" + firstGPUUUID + ",NVIDIA,4096,25:;",
			devices: []v1.DeviceAllocation{
				{UUID: firstGPUUUID, Product: product, NodeID: "node-a", VDeviceIndex: "1", MemoryMiB: 4096, CoreUnits: 25},
				{UUID: secondGPUUUID, Product: product, NodeID: "node-a", VDeviceIndex: "0", MemoryMiB: 8192, CoreUnits: 50},
			},
		},
		{
			name:       "empty_containers_and_shared_gpu_reset_device_indices",
			annotation: ";" + firstGPUUUID + ",NVIDIA,4096,25:;;" + firstGPUUUID + ",NVIDIA,8192,0:;",
			devices: []v1.DeviceAllocation{
				{UUID: firstGPUUUID, Product: product, NodeID: "node-a", VDeviceIndex: "0", MemoryMiB: 4096, CoreUnits: 25},
				{UUID: firstGPUUUID, Product: product, NodeID: "node-a", VDeviceIndex: "0", MemoryMiB: 8192, CoreUnits: 0},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			allocations, err := nvidiaKubernetesAllocations(adapter.HardwareSnapshot{}, adapter.KubernetesEvidence{
				Common:              adapter.CommonEvidence{Labels: adapter.CanonicalLabels{Node: "node-a"}},
				AllocationAvailable: true,
				NodeAnnotations:     nodeAnnotations,
				EndpointPods: []adapter.EndpointPodEvidence{{
					Namespace: "default",
					Name:      "chat-abc",
					NodeName:  "node-a",
					Labels:    map[string]string{"endpoint": "chat"},
					Annotations: map[string]string{
						"hami.io/vgpu-devices-allocated": tc.annotation,
					},
				}},
			})

			require.NoError(t, err)
			require.Len(t, allocations, 1)
			assert.Equal(t, tc.devices, allocations[0].Devices)
		})
	}
}

func TestNvidiaHAMiV210MonitorMetrics(t *testing.T) {
	const gpuUUID = "GPU-12345678-1234-1234-1234-123456789012"
	pods := map[podKey]podIdentity{
		{namespace: "default", name: "chat-abc"}: {endpoint: "chat", node: "node-a"},
	}
	cases := []struct {
		name             string
		smUtil           uint64
		utilizationRatio float64
	}{
		{name: "idle_utilization", smUtil: 0, utilizationRatio: 0},
		{name: "one_percent_utilization", smUtil: 1, utilizationRatio: 0.01},
		{name: "partial_utilization", smUtil: 75, utilizationRatio: 0.75},
		{name: "full_utilization", smUtil: 100, utilizationRatio: 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := fmt.Sprintf(`
hami_vgpu_memory_used_bytes{container="engine",device_uuid="%[1]s",namespace="default",pod="chat-abc",vdevice_index="1",zone="node-a"} 4294967296
hami_container_device_memory_bytes{container="engine",device_uuid="%[1]s",namespace="default",pod="chat-abc",vdevice_index="1",zone="node-a"} 4294967296
vGPU_device_memory_usage_in_bytes{ctrname="engine",deviceuuid="%[1]s",podname="chat-abc",podnamespace="default",vdeviceid="1",zone="node-a"} 4294967296
hami_container_device_utilization_ratio{container="engine",device_uuid="%[1]s",namespace="default",pod="chat-abc",vdevice_index="1",zone="node-a"} %[2]d
`, gpuUUID, tc.smUtil)

			usages := nvidiaHAMiEndpointReplicaUsagesFromMetrics(raw, pods)

			require.Len(t, usages, 1)
			assert.Equal(t, "node-a", usages[0].NodeID)
			assert.Equal(t, "engine", usages[0].Container)
			assert.Equal(t, gpuUUID, usages[0].AcceleratorUUID)
			assert.Equal(t, "1", usages[0].VDeviceIndex)
			require.NotNil(t, usages[0].MemoryUsedBytes)
			assert.Equal(t, float64(4294967296), *usages[0].MemoryUsedBytes)
			require.NotNil(t, usages[0].UtilizationRatio)
			assert.Equal(t, tc.utilizationRatio, *usages[0].UtilizationRatio)
		})
	}
}
