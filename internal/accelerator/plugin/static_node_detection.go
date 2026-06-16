package plugin

import (
	"context"
	"strconv"
	"strings"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type pciStaticNodeAcceleratorDetector struct {
	acceleratorType string
	vendor          string
	productName     string
	productModel    string
	resourceName    string
	match           func(string) bool
}

func detectPCIStaticNodeAccelerator(
	ctx context.Context,
	runner NodeCommandRunner,
	detector pciStaticNodeAcceleratorDetector,
) (*v1.StaticNodeAcceleratorStatus, bool, error) {
	output, err := runner.Run(ctx, "lspci -nn")
	if err != nil {
		return nil, false, err
	}

	devices := []v1.StaticNodeAcceleratorDeviceStatus{}
	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.ToLower(rawLine)
		if !detector.match(line) {
			continue
		}

		devices = append(devices, v1.StaticNodeAcceleratorDeviceStatus{
			ID:          strconv.Itoa(len(devices)),
			ProductName: detector.productName,
			Healthy:     true,
		})
	}

	if len(devices) == 0 {
		return nil, false, nil
	}

	return &v1.StaticNodeAcceleratorStatus{
		Type:           detector.acceleratorType,
		Vendor:         detector.vendor,
		ProductName:    detector.productName,
		ProductModel:   detector.productModel,
		RuntimeProfile: detector.acceleratorType,
		ResourceName:   detector.resourceName,
		Devices:        devices,
	}, true, nil
}
