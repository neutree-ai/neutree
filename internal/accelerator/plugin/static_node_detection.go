package plugin

import v1 "github.com/neutree-ai/neutree/api/v1"

func staticNodeAcceleratorResponseFromAccelerators(
	accelerators []v1.Accelerator,
	acceleratorType string,
) *v1.DetectStaticNodeAcceleratorResponse {
	if len(accelerators) == 0 {
		return &v1.DetectStaticNodeAcceleratorResponse{}
	}

	return &v1.DetectStaticNodeAcceleratorResponse{
		Matched: true,
		Accelerator: &v1.StaticNodeAcceleratorStatus{
			Type: acceleratorType,
		},
	}
}
