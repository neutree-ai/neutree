//go:build linux && cgo

package nvidia

import (
	"bytes"
	"io"
	"testing"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
	nvmlmock "github.com/NVIDIA/go-nvml/pkg/nvml/mock"
	"github.com/stretchr/testify/assert"
	"k8s.io/klog/v2"
)

func TestNvidiaNVMLOptionalProperties(t *testing.T) {
	cases := []struct {
		name     string
		ret      nvml.Return
		wantNUMA int
		wantPCIe int
		wantOK   bool
	}{
		{name: "supported NUMA node zero", ret: nvml.SUCCESS, wantNUMA: 0, wantPCIe: 4, wantOK: true},
		{name: "unsupported optional properties are empty", ret: nvml.ERROR_NOT_SUPPORTED, wantNUMA: -1, wantPCIe: -1, wantOK: true},
		{name: "lost GPU remains an error", ret: nvml.ERROR_GPU_IS_LOST},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			device := realNvidiaNVMLHardwareDevice{device: &nvmlmock.Device{
				GetNumaNodeIdFunc:            func() (int, nvml.Return) { return 0, tc.ret },
				GetMaxPcieLinkGenerationFunc: func() (int, nvml.Return) { return 4, tc.ret },
			}}
			numa, ok := device.NUMANode()
			assert.Equal(t, tc.wantOK, ok)
			pcie, ok := device.PCIEGeneration()
			assert.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				assert.Equal(t, tc.wantNUMA, numa)
				assert.Equal(t, tc.wantPCIe, pcie)
			}
		})
	}
}

func TestNvidiaNVMLCUDADriverVersionFallback(t *testing.T) {
	cases := []struct {
		name        string
		v2          nvml.Return
		v1          nvml.Return
		wantVersion string
		wantOK      bool
		wantV1Calls int
		wantLogs    int
	}{
		{name: "v2 succeeds", v2: nvml.SUCCESS, wantVersion: "12.8", wantOK: true},
		{name: "v1 fallback succeeds", v2: nvml.ERROR_FUNCTION_NOT_FOUND, v1: nvml.SUCCESS, wantVersion: "12.8", wantOK: true, wantV1Calls: 1},
		{name: "both queries fail", v2: nvml.ERROR_UNKNOWN, v1: nvml.ERROR_UNKNOWN, wantV1Calls: 1, wantLogs: 1},
		{name: "CUDA version unsupported", v2: nvml.ERROR_NOT_SUPPORTED, v1: nvml.ERROR_NOT_SUPPORTED, wantOK: true, wantV1Calls: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			state := klog.CaptureState()
			t.Cleanup(state.Restore)
			klog.LogToStderr(false)
			klog.SetOutput(io.Discard)
			klog.SetOutputBySeverity("WARNING", &logs)
			v1Calls := 0
			client := realNvidiaNVMLHardwareClient{api: &nvmlmock.Interface{
				SystemGetCudaDriverVersion_v2Func: func() (int, nvml.Return) { return 12080, tc.v2 },
				SystemGetCudaDriverVersionFunc: func() (int, nvml.Return) {
					v1Calls++
					return 12080, tc.v1
				},
			}}

			version, ok := client.CUDADriverVersion()

			assert.Equal(t, tc.wantVersion, version)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantV1Calls, v1Calls)
			klog.Flush()
			assert.Equal(t, tc.wantLogs, bytes.Count(logs.Bytes(), []byte("NVIDIA NVML")))
		})
	}
}
