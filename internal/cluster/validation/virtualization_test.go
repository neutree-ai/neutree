package validation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSupportsVirtualizationClusterVersion(t *testing.T) {
	tests := []struct {
		name      string
		version   string
		supported bool
		wantErr   bool
	}{
		{
			name:      "empty version is unsupported",
			version:   "",
			supported: false,
		},
		{
			name:      "v1 shorthand is unsupported",
			version:   "v1",
			supported: false,
		},
		{
			name:      "older patch is unsupported",
			version:   "v1.0.9",
			supported: false,
		},
		{
			name:      "minimum version is supported",
			version:   "v1.1.0",
			supported: true,
		},
		{
			name:      "nightly with minimum base version is supported",
			version:   "v1.1.0-nightly-20260603",
			supported: true,
		},
		{
			name:      "newer version is supported",
			version:   "v1.2.0",
			supported: true,
		},
		{
			name:    "invalid version returns error",
			version: "nightly",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			supported, err := SupportsVirtualizationClusterVersion(tt.version)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.supported, supported)
		})
	}
}
