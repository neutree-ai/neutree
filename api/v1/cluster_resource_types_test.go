package v1

import (
	"encoding/json"
	"testing"
)

func TestClusterResources_Serialization(t *testing.T) {
	// Create a sample ClusterResources
	clusterResources := &ClusterResources{
		ResourceStatus: ResourceStatus{
			Allocatable: &ResourceInfo{
				CPU:    96.0,
				Memory: 512.0,
				AcceleratorGroups: map[AcceleratorType]*AcceleratorGroup{
					"nvidia_gpu": {
						Quantity: 8.0,
						ProductGroups: map[AcceleratorProduct]float64{
							"Tesla-V100": 4.0,
							"Tesla-T4":   4.0,
						},
					},
				},
			},
			Available: &ResourceInfo{
				CPU:    80.0,
				Memory: 400.0,
				AcceleratorGroups: map[AcceleratorType]*AcceleratorGroup{
					"nvidia_gpu": {
						Quantity: 6.0,
						ProductGroups: map[AcceleratorProduct]float64{
							"Tesla-V100": 3.0,
							"Tesla-T4":   3.0,
						},
					},
				},
			},
		},
	}

	// Test JSON serialization
	jsonData, err := json.MarshalIndent(clusterResources, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal ClusterResources: %v", err)
	}

	t.Logf("Serialized JSON:\n%s", string(jsonData))

	// Test JSON deserialization
	var deserialized ClusterResources
	err = json.Unmarshal(jsonData, &deserialized)
	if err != nil {
		t.Fatalf("Failed to unmarshal ClusterResources: %v", err)
	}

	// Verify deserialized data
	if deserialized.Allocatable.CPU != 96.0 {
		t.Errorf("Expected Allocatable.CPU = 96.0, got %f", deserialized.Allocatable.CPU)
	}
	if deserialized.Available.Memory != 400.0 {
		t.Errorf("Expected Available.Memory = 400.0, got %f", deserialized.Available.Memory)
	}
}

func TestClusterResources_HelperMethods(t *testing.T) {
	clusterResources := &ClusterResources{
		ResourceStatus: ResourceStatus{
			Allocatable: &ResourceInfo{
				CPU:    96.0,
				Memory: 512.0,
				AcceleratorGroups: map[AcceleratorType]*AcceleratorGroup{
					"nvidia_gpu": {
						Quantity: 8.0,
					},
				},
			},
			Available: &ResourceInfo{
				CPU:    80.0,
				Memory: 400.0,
				AcceleratorGroups: map[AcceleratorType]*AcceleratorGroup{
					"nvidia_gpu": {
						Quantity: 6.0,
					},
				},
			},
		},
	}

	// Test GetTotalCPU
	if cpu := clusterResources.GetTotalCPU(); cpu != 96.0 {
		t.Errorf("Expected GetTotalCPU() = 96.0, got %f", cpu)
	}

	// Test GetAvailableCPU
	if cpu := clusterResources.GetAvailableCPU(); cpu != 80.0 {
		t.Errorf("Expected GetAvailableCPU() = 80.0, got %f", cpu)
	}

	// Test GetTotalMemory
	if memory := clusterResources.GetTotalMemory(); memory != 512.0 {
		t.Errorf("Expected GetTotalMemory() = 512.0, got %f", memory)
	}

	// Test GetAvailableMemory
	if memory := clusterResources.GetAvailableMemory(); memory != 400.0 {
		t.Errorf("Expected GetAvailableMemory() = 400.0, got %f", memory)
	}

	// Test GetTotalAccelerators
	if gpus := clusterResources.GetTotalAccelerators("nvidia_gpu"); gpus != 8.0 {
		t.Errorf("Expected GetTotalAccelerators('nvidia_gpu') = 8.0, got %f", gpus)
	}

	// Test GetAvailableAccelerators
	if gpus := clusterResources.GetAvailableAccelerators("nvidia_gpu"); gpus != 6.0 {
		t.Errorf("Expected GetAvailableAccelerators('nvidia_gpu') = 6.0, got %f", gpus)
	}

	// Test HasAcceleratorType
	if !clusterResources.HasAcceleratorType("nvidia_gpu") {
		t.Error("Expected HasAcceleratorType('nvidia_gpu') = true")
	}
	if clusterResources.HasAcceleratorType("amd_gpu") {
		t.Error("Expected HasAcceleratorType('amd_gpu') = false")
	}

	// Test GetAcceleratorTypes
	types := clusterResources.GetAcceleratorTypes()
	if len(types) != 1 || types[0] != "nvidia_gpu" {
		t.Errorf("Expected GetAcceleratorTypes() = ['nvidia_gpu'], got %v", types)
	}
}

func TestClusterResources_GetProductModels(t *testing.T) {
	clusterResources := &ClusterResources{
		ResourceStatus: ResourceStatus{
			Allocatable: &ResourceInfo{
				AcceleratorGroups: map[AcceleratorType]*AcceleratorGroup{
					"nvidia_gpu": {
						ProductGroups: map[AcceleratorProduct]float64{
							"Tesla-V100": 4.0,
							"Tesla-T4":   4.0,
						},
					},
				},
			},
		},
	}

	// Test GetProductModels for nvidia_gpu
	models := clusterResources.GetProductModels("nvidia_gpu")
	if len(models) != 2 {
		t.Errorf("Expected 2 product models, got %d", len(models))
	}

	// Test GetProductModels for non-existent type
	models = clusterResources.GetProductModels("amd_gpu")
	if models != nil {
		t.Errorf("Expected nil for non-existent accelerator type, got %v", models)
	}
}
