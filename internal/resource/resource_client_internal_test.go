package resource

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/assert"
)

func TestDeviceAvailableEquivalentQuantity(t *testing.T) {
	pool := func(memoryMiB, coreUnits int64) *v1.DeviceResourcePool {
		return &v1.DeviceResourcePool{MemoryMiB: memoryMiB, CoreUnits: coreUnits}
	}

	testCases := []struct {
		name           string
		allocatable    *v1.DeviceResourcePool
		available      *v1.DeviceResourcePool
		wantEquivalent float64
	}{
		{
			name:           "fully free device contributes one card",
			allocatable:    pool(15360, 100),
			available:      pool(15360, 100),
			wantEquivalent: 1,
		},
		{
			name:           "values above allocatable are clamped",
			allocatable:    pool(15360, 100),
			available:      pool(16384, 128),
			wantEquivalent: 1,
		},
		{
			name:           "memory ratio limits capacity",
			allocatable:    pool(15360, 100),
			available:      pool(7680, 100),
			wantEquivalent: 0.5,
		},
		{
			name:           "compute ratio limits capacity",
			allocatable:    pool(15360, 100),
			available:      pool(15360, 50),
			wantEquivalent: 0.5,
		},
		{
			name:           "smaller ratio wins when both dimensions are partial",
			allocatable:    pool(15360, 100),
			available:      pool(11520, 25),
			wantEquivalent: 0.25,
		},
		{
			name:           "fully exhausted device contributes zero",
			allocatable:    pool(15360, 100),
			available:      pool(0, 0),
			wantEquivalent: 0,
		},
		{
			name:           "negative remaining memory is clamped to zero",
			allocatable:    pool(15360, 100),
			available:      pool(-1, 100),
			wantEquivalent: 0,
		},
		{
			name:           "negative remaining compute is clamped to zero",
			allocatable:    pool(15360, 100),
			available:      pool(15360, -1),
			wantEquivalent: 0,
		},
		{
			name:           "zero allocatable dimension contributes zero",
			allocatable:    pool(0, 100),
			available:      pool(0, 100),
			wantEquivalent: 0,
		},
		{
			name:           "missing pool contributes zero",
			allocatable:    nil,
			available:      pool(15360, 100),
			wantEquivalent: 0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.wantEquivalent, deviceAvailableEquivalentQuantity(tc.allocatable, tc.available))
		})
	}
}
