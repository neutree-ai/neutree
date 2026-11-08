//go:build linux && cgo

package nvidia

import (
	"testing"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNvidiaNVMLMemoryDistinguishesUnsupportedAndFailedQueries(t *testing.T) {
	cases := []struct {
		name string
		ret  nvml.Return
		want uint64
		err  string
	}{
		{name: "available", ret: nvml.SUCCESS, want: 32 * 1024 * 1024 * 1024},
		{name: "unsupported capability", ret: nvml.ERROR_NOT_SUPPORTED},
		{name: "lost GPU", ret: nvml.ERROR_GPU_IS_LOST, err: "ret=15"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			device := realNvidiaNVMLHardwareDevice{device: fakeNVMLMemoryDevice{ret: tc.ret}}

			value, err := device.MemoryTotalBytes()

			if tc.err != "" {
				require.ErrorContains(t, err, tc.err)
				assert.Contains(t, err.Error(), "nvmlDeviceGetMemoryInfo")

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.want, value)
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
