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

func TestClusterResources_NodeDevicesOnlySerializeUnderNodeResources(t *testing.T) {
	clusterResources := &ClusterResources{
		ResourceStatus: ResourceStatus{
			Allocatable: &ResourceInfo{CPU: 16},
			Available:   &ResourceInfo{CPU: 8},
		},
		NodeResources: map[string]*NodeResourceStatus{
			"node-1": {
				ResourceStatus: ResourceStatus{
					Allocatable: &ResourceInfo{CPU: 16},
					Available:   &ResourceInfo{CPU: 8},
				},
				Devices: []*DeviceResource{
					{
						UUID:    "GPU-1",
						Product: "Tesla-T4",
						Health:  true,
					},
				},
			},
		},
	}

	jsonData, err := json.Marshal(clusterResources)
	if err != nil {
		t.Fatalf("Failed to marshal ClusterResources: %v", err)
	}

	var root map[string]interface{}
	if err := json.Unmarshal(jsonData, &root); err != nil {
		t.Fatalf("Failed to unmarshal ClusterResources JSON: %v", err)
	}

	if _, exists := root["devices"]; exists {
		t.Fatal("Expected cluster root to omit devices")
	}

	nodeResources, ok := root["node_resources"].(map[string]interface{})
	if !ok {
		t.Fatal("Expected node_resources to be an object")
	}
	nodeResource, ok := nodeResources["node-1"].(map[string]interface{})
	if !ok {
		t.Fatal("Expected node-1 resource to be an object")
	}
	if _, exists := nodeResource["devices"]; !exists {
		t.Fatal("Expected node resource to include devices")
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

	clusterResources.Allocatable.AcceleratorGroups["nvidia_gpu"].ProductGroups = map[AcceleratorProduct]float64{}
	clusterResources.Allocatable.AcceleratorGroups["nvidia_gpu"].Products = map[AcceleratorProduct]*AcceleratorProductResource{
		"Tesla-A10": {
			Quantity: 1,
		},
	}
	models = clusterResources.GetProductModels("nvidia_gpu")
	if len(models) != 1 || models[0] != "Tesla-A10" {
		t.Errorf("Expected product model fallback from Products, got %v", models)
	}
}

func TestClusterResources_ProductVirtualizationSerialization(t *testing.T) {
	clusterResources := &ClusterResources{
		AcceleratorMetadata: map[AcceleratorType]*AcceleratorMetadata{
			AcceleratorTypeNVIDIAGPU: {
				Products: map[AcceleratorProduct]*AcceleratorProductMetadata{
					"Tesla-T4": {
						MemoryTotalMiB: 15360,
					},
				},
			},
		},
		ResourceStatus: ResourceStatus{
			Allocatable: &ResourceInfo{
				AcceleratorGroups: map[AcceleratorType]*AcceleratorGroup{
					AcceleratorTypeNVIDIAGPU: {
						Quantity: 3,
						ProductGroups: map[AcceleratorProduct]float64{
							"Tesla-T4": 3,
						},
						Products: map[AcceleratorProduct]*AcceleratorProductResource{
							"Tesla-T4": {
								Quantity: 3,
								Virtualization: &AcceleratorVirtualizationResource{
									MemoryMiB: 46080,
									CoreUnits: 300,
								},
							},
						},
					},
				},
			},
		},
	}

	jsonData, err := json.Marshal(clusterResources)
	if err != nil {
		t.Fatalf("Failed to marshal ClusterResources: %v", err)
	}

	var deserialized ClusterResources
	if err := json.Unmarshal(jsonData, &deserialized); err != nil {
		t.Fatalf("Failed to unmarshal ClusterResources: %v", err)
	}

	product := deserialized.Allocatable.AcceleratorGroups[AcceleratorTypeNVIDIAGPU].Products["Tesla-T4"]
	if product.Quantity != 3 {
		t.Fatalf("Expected product quantity 3, got %f", product.Quantity)
	}
	if product.Virtualization.MemoryMiB != 46080 {
		t.Fatalf("Expected product virtualization memory 46080, got %f", product.Virtualization.MemoryMiB)
	}
	if deserialized.AcceleratorMetadata[AcceleratorTypeNVIDIAGPU].Products["Tesla-T4"].MemoryTotalMiB != 15360 {
		t.Fatalf("Expected product memory metadata 15360, got %f", deserialized.AcceleratorMetadata[AcceleratorTypeNVIDIAGPU].Products["Tesla-T4"].MemoryTotalMiB)
	}
}
