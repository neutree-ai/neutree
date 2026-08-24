package accelerator

import "testing"

func TestAcceleratorAnnotationKeys(t *testing.T) {
	if NeutreeAcceleratorDevicesAnnotation != "neutree.ai/accelerator-devices" {
		t.Fatalf("unexpected device annotation key: %q", NeutreeAcceleratorDevicesAnnotation)
	}

	if NeutreeAcceleratorAllocationsAnnotation != "neutree.ai/accelerator-allocations" {
		t.Fatalf("unexpected allocation annotation key: %q", NeutreeAcceleratorAllocationsAnnotation)
	}
}
