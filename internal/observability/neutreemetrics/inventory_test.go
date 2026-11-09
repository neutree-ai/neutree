package neutreemetrics

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

func TestMergeHardwareInventory(t *testing.T) {
	healthy := inventoryTestHardware(8)
	lost := inventoryTestHardware(8)
	lost.Accelerator.Devices[4].Healthy = false
	unhealthy := inventoryTestHardware(8)
	for i := range unhealthy.Accelerator.Devices {
		unhealthy.Accelerator.Devices[i].Healthy = false
	}
	short := inventoryTestHardware(8)
	short.Accelerator.Devices = append(short.Accelerator.Devices[:4], short.Accelerator.Devices[5:]...)
	short.Details = append(short.Details[:4], short.Details[5:]...)
	partial := inventoryTestHardware(8)
	partial.Accelerator.Devices[4] = v1.StaticNodeAcceleratorDeviceStatus{ID: "4", UUID: "GPU-4", Healthy: false}
	partial.Details = nil
	cases := []struct {
		name     string
		previous adapter.HardwareSnapshot
		fresh    adapter.HardwareSnapshot
		want     adapter.HardwareSnapshot
	}{
		{name: "initial discovery", fresh: healthy, want: healthy},
		{name: "missing GPU retains its position and identity", previous: healthy, fresh: short, want: lost},
		{name: "partial rediscovery retains hardware fields", previous: healthy, fresh: partial, want: lost},
		{name: "failed discovery marks all known GPUs unhealthy", previous: healthy, want: unhealthy},
		{name: "successful rediscovery restores health", previous: unhealthy, fresh: healthy, want: healthy},
		{name: "newly discovered GPU is appended", previous: inventoryTestHardware(1), fresh: inventoryTestHardware(2), want: inventoryTestHardware(2)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := mergeHardwareInventory(tc.previous.Clone(), tc.fresh, "vendor")

			assert.Equal(t, tc.want, result)
		})
	}
}

func inventoryTestHardware(count int) adapter.HardwareSnapshot {
	result := adapter.HardwareSnapshot{Accelerator: v1.StaticNodeAcceleratorStatus{Type: "vendor"}}
	for i := range count {
		minor := i
		result.Accelerator.Devices = append(result.Accelerator.Devices, v1.StaticNodeAcceleratorDeviceStatus{
			UUID: fmt.Sprintf("GPU-%d", i), ID: fmt.Sprint(i), MinorNumber: &minor, Healthy: true,
			ProductName: "RTX 5090", ProductModel: "RTX-5090", MemoryMiB: 32768,
		})
		result.Details = append(result.Details, adapter.HardwareDetails{UUID: fmt.Sprintf("GPU-%d", i), PCIEBusID: fmt.Sprint(i)})
	}
	return result
}
