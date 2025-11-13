package v1

import (
	"fmt"
)

// ============================================================================
// Cluster Resource Information Types
// These types are used for cluster-level resource tracking and reporting.
// They follow Kubernetes Node Status pattern for organizing resources by dimensions.
// ============================================================================

// ClusterResources represents the complete resource information of a cluster,
// organized by dimensions (Allocatable and Available).
// This follows Kubernetes Node Status pattern for consistency and clarity.
//
// Example usage:
//
//	cluster.Status.ClusterResources = &ClusterResources{
//	    Allocatable: &ResourceInfo{
//	        CPU:    96.00,
//	        Memory: 512.00,
//	        AcceleratorGroups: map[AcceleratorType]*AcceleratorGroup{
//	            "nvidia_gpu": {...},
//	        },
//	    },
//	    Available: &ResourceInfo{
//	        CPU:    80.00,
//	        Memory: 400.00,
//	        AcceleratorGroups: map[AcceleratorType]*AcceleratorGroup{
//	            "nvidia_gpu": {...},
//	        },
//	    },
//	}
type ClusterResources struct {
	// Allocatable represents the total resources that can be allocated in the cluster.
	// This corresponds to the sum of all node's allocatable resources.
	Allocatable *ResourceInfo `json:"allocatable,omitempty"`

	// Available represents the currently available (unallocated) resources in the cluster.
	// Available = Allocatable - Allocated
	Available *ResourceInfo `json:"available,omitempty"`
}

// ResourceInfo represents a complete set of resources including CPU, Memory, and Accelerators.
// All resources are organized in a flat structure for easy access and type safety.
type ResourceInfo struct {
	// CPU represents the number of CPU cores.
	// Unit: cores (e.g., 96.0 means 96 CPU cores)
	CPU float64 `json:"cpu,omitempty"`

	// Memory represents the amount of memory.
	// Unit: GiB (e.g., 512.0 means 512 GiB)
	Memory float64 `json:"memory,omitempty"`

	// AcceleratorGroups contains accelerator resources grouped by type.
	// Key: accelerator type (e.g., "nvidia_gpu", "amd_gpu", "neuron")
	// Value: AcceleratorGroup containing details for that accelerator type
	//
	// Example:
	//   {
	//     "nvidia_gpu": {...},
	//     "amd_gpu": {...}
	//   }
	AcceleratorGroups map[AcceleratorType]*AcceleratorGroup `json:"accelerator_groups,omitempty"`
}

// AcceleratorGroup represents accelerator resources grouped by type.
// It supports heterogeneous clusters where multiple accelerator types can coexist.
//
// Example for a cluster with NVIDIA GPUs:
//
//	{
//	    Quantity: 8.0,
//	    ProductGroups: map[string]*ProductGroup{
//	        "Tesla-V100": 4,
//	        "Tesla-T4":   4,
//	    },
//	}
type AcceleratorGroup struct {
	// Quantity is the total number of accelerators of this type.
	// Unit: count (e.g., 8.0 means 8 accelerators)
	Quantity float64 `json:"quantity,omitempty"`

	// ProductGroups contains accelerators further grouped by product model.
	// This enables fine-grained resource tracking for heterogeneous accelerator types.
	// Key: product model name (e.g., "Tesla-V100", "Tesla-T4", "MI100")
	// Value: ProductGroup containing details for that product model
	//
	// Example for NVIDIA GPUs:
	//   {
	//     "Tesla-V100": 4,
	//     "Tesla-T4": 4
	//   }
	ProductGroups map[AcceleratorProduct]float64 `json:"product_groups,omitempty"`
}

// ============================================================================
// Helper Methods for ClusterResources
// ============================================================================

// GetTotalCPU returns the total allocatable CPU in the cluster.
func (c *ClusterResources) GetTotalCPU() float64 {
	if c == nil || c.Allocatable == nil {
		return 0
	}

	return c.Allocatable.CPU
}

// GetAvailableCPU returns the available CPU in the cluster.
func (c *ClusterResources) GetAvailableCPU() float64 {
	if c == nil || c.Available == nil {
		return 0
	}

	return c.Available.CPU
}

// GetTotalMemory returns the total allocatable memory in the cluster (in GiB).
func (c *ClusterResources) GetTotalMemory() float64 {
	if c == nil || c.Allocatable == nil {
		return 0
	}

	return c.Allocatable.Memory
}

// GetAvailableMemory returns the available memory in the cluster (in GiB).
func (c *ClusterResources) GetAvailableMemory() float64 {
	if c == nil || c.Available == nil {
		return 0
	}

	return c.Available.Memory
}

// GetTotalAccelerators returns the total number of accelerators of a specific type.
func (c *ClusterResources) GetTotalAccelerators(acceleratorType string) float64 {
	if c == nil || c.Allocatable == nil || c.Allocatable.AcceleratorGroups == nil {
		return 0
	}

	if group, ok := c.Allocatable.AcceleratorGroups[AcceleratorType(acceleratorType)]; ok {
		return group.Quantity
	}

	return 0
}

// GetAvailableAccelerators returns the available number of accelerators of a specific type.
func (c *ClusterResources) GetAvailableAccelerators(acceleratorType string) float64 {
	if c == nil || c.Available == nil || c.Available.AcceleratorGroups == nil {
		return 0
	}

	if group, ok := c.Available.AcceleratorGroups[AcceleratorType(acceleratorType)]; ok {
		return group.Quantity
	}

	return 0
}

// HasAcceleratorType checks if the cluster has a specific accelerator type.
func (c *ClusterResources) HasAcceleratorType(acceleratorType string) bool {
	if c == nil || c.Allocatable == nil || c.Allocatable.AcceleratorGroups == nil {
		return false
	}

	_, ok := c.Allocatable.AcceleratorGroups[AcceleratorType(acceleratorType)]

	return ok
}

// GetAcceleratorTypes returns all accelerator types available in the cluster.
func (c *ClusterResources) GetAcceleratorTypes() []string {
	if c == nil || c.Allocatable == nil || c.Allocatable.AcceleratorGroups == nil {
		return nil
	}

	types := make([]string, 0, len(c.Allocatable.AcceleratorGroups))
	for t := range c.Allocatable.AcceleratorGroups {
		types = append(types, string(t))
	}

	return types
}

// GetProductModels returns all product models for a specific accelerator type.
func (c *ClusterResources) GetProductModels(acceleratorType string) []string {
	if c == nil || c.Allocatable == nil || c.Allocatable.AcceleratorGroups == nil {
		return nil
	}

	group, ok := c.Allocatable.AcceleratorGroups[AcceleratorType(acceleratorType)]
	if !ok || group.ProductGroups == nil {
		return nil
	}

	models := make([]string, 0, len(group.ProductGroups))
	for m := range group.ProductGroups {
		models = append(models, string(m))
	}

	return models
}

func (c *ClusterResources) String() string {
	if c == nil {
		return "ClusterResources: <nil>"
	}

	output := "ClusterResources:\n"
	if c.Allocatable != nil {
		output += fmt.Sprintf("  Allocatable: CPU=%.2f, Memory=%.2f GiB\n",
			c.GetTotalCPU(), c.GetTotalMemory())
		for accType, group := range c.Allocatable.AcceleratorGroups {
			output += fmt.Sprintf("    %s: Quantity=%.2f\n", accType, group.Quantity)
			for product, quantity := range group.ProductGroups {
				output += fmt.Sprintf("      %s: Quantity=%.2f\n", product, quantity)
			}
		}
	} else {
		output += "  Allocatable: <nil>\n"
	}

	if c.Available != nil {
		output += fmt.Sprintf("  Available:   CPU=%.2f, Memory=%.2f GiB\n",
			c.GetAvailableCPU(), c.GetAvailableMemory())
		for accType, group := range c.Available.AcceleratorGroups {
			output += fmt.Sprintf("    %s: Quantity=%.2f\n", accType, group.Quantity)
			for product, quantity := range group.ProductGroups {
				output += fmt.Sprintf("      %s: Quantity=%.2f\n", product, quantity)
			}
		}
	} else {
		output += "  Available: <nil>\n"
	}

	return output
}
