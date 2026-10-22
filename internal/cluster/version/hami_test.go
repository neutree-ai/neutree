package version

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSupportsVirtualizationClusterVersion(test *testing.T) {
	cases := []struct {
		name      string
		version   string
		supported bool
		wantErr   bool
	}{
		{name: "empty version is unsupported"},
		{name: "whitespace version is unsupported", version: " \t\n"},
		{name: "v1 shorthand is unsupported", version: "v1"},
		{name: "older patch is unsupported", version: "v1.0.9"},
		{name: "minimum version is supported", version: "v1.1.0", supported: true},
		{name: "nightly with minimum base version is supported", version: "v1.1.0-nightly-20260603", supported: true},
		{name: "newer version is supported", version: "v1.2.0", supported: true},
		{name: "version without prefix is supported", version: "1.1.0", supported: true},
		{name: "minor shorthand uses base version", version: "v1.1", supported: true},
		{name: "prerelease and metadata use base version", version: "v1.1.0-rc.1+build.2", supported: true},
		{name: "surrounding whitespace is trimmed", version: " v1.1.0 \t", supported: true},
		{name: "future version is supported", version: "v2.0.0", supported: true},
		{name: "invalid version returns error", version: "nightly", wantErr: true},
	}

	for _, testCase := range cases {
		test.Run(testCase.name, func(test *testing.T) {
			supported, err := SupportsVirtualizationClusterVersion(testCase.version)
			if testCase.wantErr {
				assert.Error(test, err)
				return
			}

			require.NoError(test, err)
			assert.Equal(test, testCase.supported, supported)
		})
	}
}
