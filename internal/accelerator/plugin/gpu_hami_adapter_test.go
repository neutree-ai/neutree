package plugin

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	resourceview "github.com/neutree-ai/neutree/internal/resource"
	"github.com/stretchr/testify/require"
)

func TestGPUHAMiResourceAdapter_ParseKubernetesNode(t *testing.T) {
	adapter := &GPUHAMiResourceAdapter{}
	input := resourceview.KubernetesNodeResourceContext{
		NodeName: "gpu-node",
		Labels: map[string]string{
			NvidiaGPUKubernetesNodeSelectorKey: "Tesla-T4",
			NvidiaGPUVirtualizationLabelKey:    "true",
			NvidiaGPUCountResource:             "2",
		},
		Annotations: map[string]string{
			HAMiNodeNvidiaRegisterAnnotation: `[
				{"id":"GPU-1","count":100,"devmem":15360,"devcore":100,"type":"NVIDIA-Tesla T4","health":true},
				{"id":"GPU-2","count":100,"devmem":15360,"devcore":100,"type":"NVIDIA-Tesla T4","health":true}
			]`,
		},
		Pods: []resourceview.KubernetesPodResourceContext{
			{
				Namespace: "default",
				Name:      "infer-1",
				Annotations: map[string]string{
					HAMiVGPUDevicesAllocatedAnnotation: ";GPU-1,NVIDIA,15360,100:;",
				},
			},
		},
	}

	require.True(t, adapter.MatchKubernetesNode(input))
	result, err := adapter.ParseKubernetesNode(input)

	require.NoError(t, err)
	require.Len(t, result.Devices, 2)
	require.Equal(t, int64(15360), result.Devices[0].Allocatable.MemoryMiB)
	require.Equal(t, int64(0), result.Devices[0].Available.MemoryMiB)
	require.Equal(t, int64(15360), result.Devices[1].Available.MemoryMiB)

	allocatable := result.Allocatable.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU]
	require.Equal(t, float64(2), allocatable.Quantity)
	require.Equal(t, float64(2), allocatable.ProductGroups["Tesla-T4"])
	require.Equal(t, float64(30720), allocatable.Products["Tesla-T4"].Virtualization.MemoryMiB)
	require.Equal(t, float64(200), allocatable.Products["Tesla-T4"].Virtualization.CoreUnits)

	available := result.Available.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU]
	require.Equal(t, float64(1), available.Quantity)
	require.Equal(t, float64(1), available.ProductGroups["Tesla-T4"])
	require.Equal(t, float64(15360), available.Products["Tesla-T4"].Virtualization.MemoryMiB)
	require.Equal(t, float64(100), available.Products["Tesla-T4"].Virtualization.CoreUnits)

	metadata := result.AcceleratorMetadata[v1.AcceleratorTypeNVIDIAGPU]
	require.Equal(t, float64(15360), metadata.Products["Tesla-T4"].MemoryTotalMiB)
}

func TestGPUResourceParser_ParseKubernetesVirtualizationNode(t *testing.T) {
	parser := &GPUResourceParser{}
	input := resourceview.KubernetesNodeResourceContext{
		NodeName: "gpu-node",
		Labels: map[string]string{
			NvidiaGPUKubernetesNodeSelectorKey: "Tesla-T4",
			NvidiaGPUVirtualizationLabelKey:    "true",
		},
		Annotations: map[string]string{
			HAMiNodeNvidiaRegisterAnnotation: `[
				{"id":"GPU-1","count":100,"devmem":15360,"devcore":100,"type":"NVIDIA-Tesla T4","health":true}
			]`,
		},
	}

	result, matched, err := parser.ParseKubernetesVirtualizationNode(input)

	require.NoError(t, err)
	require.True(t, matched)
	require.Len(t, result.Devices, 1)
	group := result.Allocatable.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU]
	require.Equal(t, float64(1), group.Quantity)
	require.Equal(t, float64(15360), group.Products["Tesla-T4"].Virtualization.MemoryMiB)
	require.Equal(t, float64(100), group.Products["Tesla-T4"].Virtualization.CoreUnits)
}

func TestGPUResourceParser_ParseKubernetesVirtualizationNodeDoesNotMatchStandardNode(t *testing.T) {
	parser := &GPUResourceParser{}

	result, matched, err := parser.ParseKubernetesVirtualizationNode(resourceview.KubernetesNodeResourceContext{
		Labels: map[string]string{
			NvidiaGPUKubernetesNodeSelectorKey: "Tesla-T4",
		},
	})

	require.NoError(t, err)
	require.False(t, matched)
	require.Nil(t, result)
}

func TestGPUResourceParser_ParseKubernetesVirtualizationNodeMatchesEmptyHAMiRegistration(t *testing.T) {
	parser := &GPUResourceParser{}

	result, matched, err := parser.ParseKubernetesVirtualizationNode(resourceview.KubernetesNodeResourceContext{
		Labels: map[string]string{
			NvidiaGPUVirtualizationLabelKey: "true",
			NvidiaGPUCountResource:          "2",
		},
	})

	require.NoError(t, err)
	require.True(t, matched)
	require.NotNil(t, result)
	require.Nil(t, result.Allocatable)
	require.Nil(t, result.Available)
	require.Empty(t, result.Devices)
}

func TestGPUHAMiResourceAdapter_DoesNotMatchWithoutRegisteredDevices(t *testing.T) {
	adapter := &GPUHAMiResourceAdapter{}

	require.False(t, adapter.MatchKubernetesNode(resourceview.KubernetesNodeResourceContext{
		Labels: map[string]string{
			NvidiaGPUVirtualizationLabelKey: "true",
		},
	}))
}

func TestGPUHAMiResourceAdapter_ParseKubernetesEndpoint(t *testing.T) {
	adapter := &GPUHAMiResourceAdapter{}
	input := resourceview.KubernetesEndpointResourceContext{
		EndpointName: "chat",
		Namespace:    "default",
		Nodes: map[string]resourceview.KubernetesEndpointNodeResourceContext{
			"gpu-node": {
				Name: "gpu-node",
				Labels: map[string]string{
					NvidiaGPUKubernetesNodeSelectorKey: "Tesla-T4",
					NvidiaGPUVirtualizationLabelKey:    "true",
				},
				Annotations: map[string]string{
					HAMiNodeNvidiaRegisterAnnotation: `[
						{"id":"GPU-1","count":100,"devmem":15360,"devcore":100,"type":"NVIDIA-Tesla T4","health":true},
						{"id":"GPU-2","count":100,"devmem":15360,"devcore":100,"type":"NVIDIA-Tesla T4","health":true}
					]`,
				},
			},
		},
		Pods: []resourceview.KubernetesPodResourceContext{
			{
				Namespace: "default",
				Name:      "chat-abc",
				UID:       "uid-1",
				NodeName:  "gpu-node",
				Annotations: map[string]string{
					HAMiVGPUDevicesAllocatedAnnotation: ";GPU-1,NVIDIA,15360,100:;GPU-2,NVIDIA,7680,50:;",
				},
			},
		},
	}

	require.True(t, adapter.MatchKubernetesEndpoint(input))
	instances, err := adapter.ParseKubernetesEndpoint(input)

	require.NoError(t, err)
	require.Len(t, instances, 1)
	require.Equal(t, "chat-abc", instances[0].InstanceID)
	require.Equal(t, "chat-abc", instances[0].ReplicaID)
	require.Equal(t, "gpu-node", instances[0].NodeID)
	require.Len(t, instances[0].Devices, 2)
	require.Equal(t, "GPU-1", instances[0].Devices[0].UUID)
	require.Equal(t, "Tesla-T4", instances[0].Devices[0].Product)
	require.Equal(t, int64(15360), instances[0].Devices[0].MemoryMiB)
	require.Equal(t, int64(100), instances[0].Devices[0].CoreUnits)
	require.Equal(t, "gpu-node", instances[0].Devices[0].NodeID)
	require.Equal(t, "GPU-2", instances[0].Devices[1].UUID)
	require.Equal(t, int64(7680), instances[0].Devices[1].MemoryMiB)
	require.Equal(t, int64(50), instances[0].Devices[1].CoreUnits)
}
