package version

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSupportsVirtualizationClusterVersion(t *testing.T) {
	tt := []struct {
		name      string
		version   string
		supported bool
		wantErr   bool
	}{
		{name: "empty version is unsupported"},
		{name: "v1 shorthand is unsupported", version: "v1"},
		{name: "older release is unsupported", version: "v1.0.1"},
		{name: "minimum version is supported", version: "v1.1.0", supported: true},
		{name: "nightly with minimum base version is supported", version: "v1.1.0-nightly-20260418", supported: true},
		{name: "newer version is supported", version: "v1.2.0", supported: true},
		{name: "prerelease uses base version", version: "v1.1.0-rc.1", supported: true},
		{name: "invalid version returns error", version: "nightly", wantErr: true},
	}

	for _, testCase := range tt {
		t.Run(testCase.name, func(t *testing.T) {
			supported, err := SupportsVirtualizationClusterVersion(testCase.version)
			if testCase.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, testCase.supported, supported)
		})
	}
}
