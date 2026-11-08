//go:build linux && cgo

package nvidia

import (
	"testing"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
	"github.com/stretchr/testify/assert"
)

func TestNvidiaNVMLMemoryReportsQueryFailure(t *testing.T) {
	cases := []struct {
		name string
		ret  nvml.Return
		ok   bool
	}{
		{name: "available", ret: nvml.SUCCESS, ok: true},
		{name: "lost GPU", ret: nvml.ERROR_GPU_IS_LOST},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			device := realNvidiaNVMLHardwareDevice{device: fakeNVMLMemoryDevice{ret: tc.ret}}

			value, ok := device.MemoryTotalBytes()

			assert.Equal(t, tc.ok, ok)
			if ok {
				assert.Equal(t, uint64(32*1024*1024*1024), value)
			}
		})
	}
}

type fakeNVMLMemoryDevice struct {
	nvml.Device
	ret nvml.Return
}

func (d fakeNVMLMemoryDevice) GetMemoryInfo() (nvml.Memory, nvml.Return) {
	return nvml.Memory{Total: 32 * 1024 * 1024 * 1024}, d.ret
}
