package plugin

import (
	"strconv"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type staticNodeAcceleratorInfo struct {
	acceleratorType string
	vendor          string
	productName     string
	productModel    string
}

func staticNodeAcceleratorResponseFromAccelerators(
	accelerators []v1.Accelerator,
	info staticNodeAcceleratorInfo,
) *v1.DetectStaticNodeAcceleratorResponse {
	if len(accelerators) == 0 {
		return &v1.DetectStaticNodeAcceleratorResponse{}
	}

	devices := make([]v1.StaticNodeAcceleratorDeviceStatus, 0, len(accelerators))

	for index, accelerator := range accelerators {
		id := accelerator.ID
		if id == "" {
			id = strconv.Itoa(index)
		}

		devices = append(devices, v1.StaticNodeAcceleratorDeviceStatus{
			ID:           id,
			ProductName:  info.productName,
			ProductModel: info.productModel,
			Healthy:      true,
		})
	}

	return &v1.DetectStaticNodeAcceleratorResponse{
		Matched: true,
		Accelerator: &v1.StaticNodeAcceleratorStatus{
			Type:         info.acceleratorType,
			Vendor:       info.vendor,
			ProductName:  info.productName,
			ProductModel: info.productModel,
			Devices:      devices,
		},
	}
}
