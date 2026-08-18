package resource

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/assert"
)

func TestIsDeviceFullyAvailable(t *testing.T) {
	pool := func(memoryMiB, coreUnits int64) *v1.DeviceResourcePool {
		return &v1.DeviceResourcePool{MemoryMiB: memoryMiB, CoreUnits: coreUnits}
	}

	testCases := []struct {
		name          string
		allocatable   *v1.DeviceResourcePool
		available     *v1.DeviceResourcePool
		wantFullyFree bool
	}{
		{
			name:          "fully free matches allocatable",
			allocatable:   pool(15360, 100),
			available:     pool(15360, 100),
			wantFullyFree: true,
		},
		{
			name:          "more available than allocatable still fully free",
			allocatable:   pool(15360, 100),
			available:     pool(16384, 128),
			wantFullyFree: true,
		},
		{
			name:          "memory partially allocated is used",
			allocatable:   pool(15360, 100),
			available:     pool(7680, 100),
			wantFullyFree: false,
		},
		{
			name:          "compute partially allocated is used",
			allocatable:   pool(15360, 100),
			available:     pool(15360, 50),
			wantFullyFree: false,
		},
		{
			name:          "fully exhausted is used",
			allocatable:   pool(15360, 100),
			available:     pool(0, 0),
			wantFullyFree: false,
		},
		{
			name:          "nil allocatable is not fully free",
			allocatable:   nil,
			available:     pool(15360, 100),
			wantFullyFree: false,
		},
		{
			name:          "nil available is not fully free",
			allocatable:   pool(15360, 100),
			available:     nil,
			wantFullyFree: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.wantFullyFree, isDeviceFullyAvailable(tc.allocatable, tc.available))
		})
	}
}
