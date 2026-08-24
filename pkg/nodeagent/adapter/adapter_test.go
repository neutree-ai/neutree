package adapter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestKubernetesEvidenceCloneDoesNotShareNestedValues(t *testing.T) {
	original := KubernetesEvidence{
		Common: CommonEvidence{EndpointReplicaAcceleratorUsages: []EndpointReplicaAcceleratorUsage{{
			AcceleratorUUID: "device-a",
			MemoryUsedBytes: float64Pointer(1024),
		}}},
		PodResources: []PodResource{{
			Namespace: "default",
			Name:      "chat-a",
			Containers: []ContainerDevices{{
				ResourceName: "vendor.example/accelerator",
				DeviceIDs:    []string{"device-a"},
			}},
		}},
		EndpointPods: []EndpointPodEvidence{{
			Name:        "chat-a",
			Labels:      map[string]string{"endpoint": "chat"},
			Annotations: map[string]string{"vendor.example/raw": "value"},
		}},
	}

	cloned := original.Clone()
	cloned.PodResources[0].Containers[0].DeviceIDs[0] = "mutated"
	cloned.EndpointPods[0].Labels["endpoint"] = "changed"
	cloned.EndpointPods[0].Annotations["vendor.example/raw"] = "changed"
	*cloned.Common.EndpointReplicaAcceleratorUsages[0].MemoryUsedBytes = 2048

	assert.Equal(t, "device-a", original.PodResources[0].Containers[0].DeviceIDs[0])
	assert.Equal(t, "chat", original.EndpointPods[0].Labels["endpoint"])
	assert.Equal(t, "value", original.EndpointPods[0].Annotations["vendor.example/raw"])
	assert.Equal(t, 1024.0, *original.Common.EndpointReplicaAcceleratorUsages[0].MemoryUsedBytes)
}

func float64Pointer(value float64) *float64 {
	return &value
}

func TestStaticEvidenceCloneDoesNotShareNestedValues(t *testing.T) {
	original := StaticEvidence{RayEvidence: RayEvidence{
		Actors: []RayActor{{
			ActorID:           "actor-a",
			RequiredResources: map[string]float64{"GPU": 1},
		}},
		ActorProcesses: map[int]ProcessInfo{
			123: {
				PID:            123,
				DescendantPIDs: []int{123, 124},
				Environment:    map[string]string{"CUDA_VISIBLE_DEVICES": "0"},
			},
		},
	}}

	cloned := original.Clone()
	cloned.RayEvidence.Actors[0].RequiredResources["GPU"] = 0.5
	cloned.RayEvidence.ActorProcesses[123] = ProcessInfo{Environment: map[string]string{"CUDA_VISIBLE_DEVICES": "1"}}

	assert.Equal(t, 1.0, original.RayEvidence.Actors[0].RequiredResources["GPU"])
	assert.Equal(t, "0", original.RayEvidence.ActorProcesses[123].Environment["CUDA_VISIBLE_DEVICES"])
}

func TestMetricResultCloneDoesNotShareDevicesOrLabels(t *testing.T) {
	order := 1
	original := MetricResult{
		Allocations: []v1.StaticNodeAllocationStatus{{
			Devices: []v1.DeviceAllocation{{UUID: "device-a", Order: &order}},
		}},
		Samples: []Sample{{Name: "neutree_vendor_metric", Labels: map[string]string{"device": "device-a"}}},
	}

	cloned := original.Clone()
	*cloned.Allocations[0].Devices[0].Order = 2
	cloned.Samples[0].Labels["device"] = "device-b"

	require.NotNil(t, original.Allocations[0].Devices[0].Order)
	assert.Equal(t, 1, *original.Allocations[0].Devices[0].Order)
	assert.Equal(t, "device-a", original.Samples[0].Labels["device"])
}
