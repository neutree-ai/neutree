package nvidia

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

const (
	nvidiaHAMiVGPUDevicesAllocated = "hami.io/vgpu-devices-allocated"
	nvidiaHAMiNodeNvidiaRegister   = "hami.io/node-nvidia-register"
	nvidiaGPUProductLabel          = "nvidia.com/gpu.product"
	nvidiaHAMiAllocationFieldCount = 4
)

type nvidiaHAMiRegisteredDevice struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

func nvidiaHAMiDeviceProducts(
	evidence adapter.KubernetesEvidence,
	hardware adapter.HardwareSnapshot,
) (map[string]string, error) {
	products := make(map[string]string)

	if raw := strings.TrimSpace(evidence.NodeAnnotations[nvidiaHAMiNodeNvidiaRegister]); raw != "" {
		var devices []nvidiaHAMiRegisteredDevice
		if err := json.Unmarshal([]byte(raw), &devices); err != nil {
			return nil, fmt.Errorf("parse %s: %w", nvidiaHAMiNodeNvidiaRegister, err)
		}

		for _, device := range devices {
			if device.ID != "" && device.Type != "" {
				products[device.ID] = device.Type
			}
		}
	}

	if product := strings.TrimSpace(evidence.NodeLabels[nvidiaGPUProductLabel]); product != "" {
		for uuid := range products {
			products[uuid] = product
		}
	}

	for _, device := range hardware.Accelerator.Devices {
		if device.UUID == "" || products[device.UUID] != "" {
			continue
		}

		products[device.UUID] = strings.ReplaceAll(
			strings.TrimSpace(firstNonEmpty(device.ProductModel, device.ProductName)),
			" ",
			"-",
		)
	}

	return products, nil
}

func nvidiaHAMiDeviceAllocations(
	value string,
	nodeID string,
	products map[string]string,
) ([]v1.DeviceAllocation, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}

	devices := make([]v1.DeviceAllocation, 0)

	for _, entry := range strings.Split(value, ";") {
		vdeviceIndex := 0

		for _, segment := range strings.Split(entry, ":") {
			segment = strings.TrimSpace(segment)
			if segment == "" {
				continue
			}

			fields := strings.Split(segment, ",")
			if len(fields) < nvidiaHAMiAllocationFieldCount {
				continue
			}

			memoryMiB, err := strconv.ParseInt(strings.TrimSpace(fields[2]), 10, 64)
			if err != nil {
				return nil, fmt.Errorf("parse HAMi memory value %q: %w", fields[2], err)
			}

			coreUnits, err := strconv.ParseInt(strings.TrimSpace(fields[3]), 10, 64)
			if err != nil {
				return nil, fmt.Errorf("parse HAMi core value %q: %w", fields[3], err)
			}

			uuid := strings.TrimSpace(fields[0])
			devices = append(devices, v1.DeviceAllocation{
				UUID:         uuid,
				Product:      firstNonEmpty(products[uuid], strings.TrimSpace(fields[1])),
				NodeID:       nodeID,
				VDeviceIndex: strconv.Itoa(vdeviceIndex),
				MemoryMiB:    memoryMiB,
				CoreUnits:    coreUnits,
			})
			vdeviceIndex++
		}
	}

	sort.SliceStable(devices, func(i, j int) bool {
		return devices[i].UUID < devices[j].UUID
	})

	return devices, nil
}
