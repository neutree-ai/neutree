package version

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSupportsVirtualizationClusterVersion(test *testing.T) {
	cases := []struct {
		name           string
		clusterVersion string
		supported      bool
		wantError      bool
	}{
		{name: "empty version"},
		{name: "whitespace version", clusterVersion: " \t\n "},
		{name: "major shorthand below minimum", clusterVersion: "v1"},
		{name: "older patch", clusterVersion: "v1.0.9"},
		{name: "older prerelease with metadata", clusterVersion: "v1.0.99-rc.1+build.1"},
		{name: "minimum version", clusterVersion: "v1.1.0", supported: true},
		{name: "minimum prerelease", clusterVersion: "v1.1.0-rc.1", supported: true},
		{name: "minimum nightly", clusterVersion: "v1.1.0-nightly-20260603", supported: true},
		{name: "minimum without prefix", clusterVersion: "1.1.0+build.1", supported: true},
		{name: "trimmed newer version", clusterVersion: " v1.2.99-rc.1+build.1 ", supported: true},
		{name: "newer major", clusterVersion: "v2.0.0", supported: true},
		{name: "invalid version", clusterVersion: "nightly", wantError: true},
		{name: "invalid numeric version", clusterVersion: "v1.1.0.1", wantError: true},
	}

	for _, testCase := range cases {
		test.Run(testCase.name, func(test *testing.T) {
			supported, err := SupportsVirtualizationClusterVersion(testCase.clusterVersion)
			if testCase.wantError {
				require.Error(test, err)
				assert.False(test, supported)
				return
			}
			require.NoError(test, err)
			assert.Equal(test, testCase.supported, supported)
		})
	}
}

func TestHAMiVersionForCluster(test *testing.T) {
	cases := []struct {
		name           string
		clusterVersion string
		hamiVersion    string
	}{
		{name: "minimum prerelease", clusterVersion: "v1.1.0-rc.1", hamiVersion: "v2.9.0"},
		{name: "minimum version", clusterVersion: "v1.1.0", hamiVersion: "v2.9.0"},
		{name: "legacy patch", clusterVersion: "v1.1.1", hamiVersion: "v2.9.0"},
		{name: "legacy minor without prefix", clusterVersion: "1.2.0", hamiVersion: "v2.9.0"},
		{name: "trimmed legacy prerelease", clusterVersion: " v1.2.99-rc.1+build.1 ", hamiVersion: "v2.9.0"},
		{name: "new HAMi prerelease boundary", clusterVersion: "v1.3.0-rc.1", hamiVersion: "v2.10.0"},
		{name: "new HAMi metadata boundary", clusterVersion: "1.3.0+build.1", hamiVersion: "v2.10.0"},
		{name: "new HAMi prerelease with metadata", clusterVersion: " v1.3.0-rc.1+build.1 ", hamiVersion: "v2.10.0"},
		{name: "new HAMi release boundary", clusterVersion: "v1.3.0", hamiVersion: "v2.10.0"},
		{name: "newer patch", clusterVersion: "v1.3.1", hamiVersion: "v2.10.0"},
		{name: "newer minor", clusterVersion: "v1.4.0", hamiVersion: "v2.10.0"},
		{name: "newer major", clusterVersion: "v2.0.0", hamiVersion: "v2.10.0"},
		{name: "empty version"},
		{name: "whitespace version", clusterVersion: " "},
		{name: "invalid version", clusterVersion: "invalid"},
		{name: "below minimum", clusterVersion: "v1.0.9"},
		{name: "major shorthand below minimum", clusterVersion: "v1"},
	}

	for _, testCase := range cases {
		test.Run(testCase.name, func(test *testing.T) {
			hamiVersion, err := HAMiVersionForCluster(testCase.clusterVersion)
			if testCase.hamiVersion == "" {
				require.Error(test, err)
				assert.Empty(test, hamiVersion)
				return
			}
			require.NoError(test, err)
			assert.Equal(test, testCase.hamiVersion, hamiVersion)
		})
	}
}
