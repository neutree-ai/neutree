package adapter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestKubernetesEvidenceCloneDoesNotShareNestedValues(t *testing.T) {
	original := KubernetesEvidence{
		VirtualizationMonitor: VirtualizationMonitorEvidence{
			Text: "hami_vgpu_memory_used_bytes{pod=\"chat-a\"} 1024\n",
			Up:   true,
		},
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
		NodeLabels:      map[string]string{"vendor.example/model": "raw-model"},
		NodeAnnotations: map[string]string{"vendor.example/metadata": "raw"},
	}

	cloned := original.Clone()
	cloned.PodResources[0].Containers[0].DeviceIDs[0] = "mutated"
	cloned.EndpointPods[0].Labels["endpoint"] = "changed"
	cloned.EndpointPods[0].Annotations["vendor.example/raw"] = "changed"
	cloned.NodeLabels["vendor.example/model"] = "changed"
	cloned.NodeAnnotations["vendor.example/metadata"] = "changed"

	assert.Equal(t, "device-a", original.PodResources[0].Containers[0].DeviceIDs[0])
	assert.Equal(t, "chat", original.EndpointPods[0].Labels["endpoint"])
	assert.Equal(t, "value", original.EndpointPods[0].Annotations["vendor.example/raw"])
	assert.Equal(t, "raw-model", original.NodeLabels["vendor.example/model"])
	assert.Equal(t, "raw", original.NodeAnnotations["vendor.example/metadata"])
	assert.Equal(t, 1024.0, *original.Common.EndpointReplicaAcceleratorUsages[0].MemoryUsedBytes)
	assert.True(t, cloned.VirtualizationMonitor.Up)
	assert.Equal(t, original.VirtualizationMonitor.Text, cloned.VirtualizationMonitor.Text)
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
		Replicas: []RayReplica{{
			ActorID:           "actor-a",
			DeploymentOptions: map[string]interface{}{"num_gpus": 0.5, "nested": map[string]interface{}{"value": "original"}},
		}},
		ActorProcesses: map[int]ProcessInfo{
			123: {
				PID:            123,
				DescendantPIDs: []int{123, 124},
				Environment:    map[string]string{"CUDA_VISIBLE_DEVICES": "0"},
			},
		},
		AcceleratorProcesses: []AcceleratorProcess{{
			DeviceID:        "device-a",
			PID:             124,
			MemoryUsedBytes: float64Pointer(1024),
		}},
	}}

	cloned := original.Clone()
	cloned.RayEvidence.Actors[0].RequiredResources["GPU"] = 0.5
	cloned.RayEvidence.Replicas[0].DeploymentOptions["num_gpus"] = 1.0
	cloned.RayEvidence.Replicas[0].DeploymentOptions["nested"].(map[string]interface{})["value"] = "changed"
	cloned.RayEvidence.ActorProcesses[123] = ProcessInfo{Environment: map[string]string{"CUDA_VISIBLE_DEVICES": "1"}}
	*cloned.RayEvidence.AcceleratorProcesses[0].MemoryUsedBytes = 2048

	assert.Equal(t, 1.0, original.RayEvidence.Actors[0].RequiredResources["GPU"])
	assert.Equal(t, 0.5, original.RayEvidence.Replicas[0].DeploymentOptions["num_gpus"])
	assert.Equal(t, "original", original.RayEvidence.Replicas[0].DeploymentOptions["nested"].(map[string]interface{})["value"])
	assert.Equal(t, "0", original.RayEvidence.ActorProcesses[123].Environment["CUDA_VISIBLE_DEVICES"])
	assert.Equal(t, 1024.0, *original.RayEvidence.AcceleratorProcesses[0].MemoryUsedBytes)
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

func TestHardwareSnapshotCloneDoesNotShareDeviceAliases(t *testing.T) {
	original := HardwareSnapshot{Details: []HardwareDetails{{
		UUID:              "device-a",
		DeviceAliases:     []string{"logical-7"},
		CUDACapability:    "8.9",
		CUDADriverVersion: "12.8",
	}}}

	cloned := original.Clone()
	cloned.Details[0].DeviceAliases[0] = "logical-8"

	assert.Equal(t, "logical-7", original.Details[0].DeviceAliases[0])
	assert.Equal(t, "8.9", cloned.Details[0].CUDACapability)
	assert.Equal(t, "12.8", cloned.Details[0].CUDADriverVersion)
}
