package accelerator

const (
	// NeutreeAcceleratorDevicesAnnotation stores the normalized accelerator
	// inventory reported for a Kubernetes node.
	NeutreeAcceleratorDevicesAnnotation = "neutree.ai/accelerator-devices"
	// NeutreeAcceleratorAllocationsAnnotation stores the normalized accelerator
	// allocation records reported for a Kubernetes pod.
	NeutreeAcceleratorAllocationsAnnotation = "neutree.ai/accelerator-allocations"
)
