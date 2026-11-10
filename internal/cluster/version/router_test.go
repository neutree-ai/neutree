package version

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSupportsRouterClientConfig(t *testing.T) {
	cases := []struct {
		name      string
		version   string
		supported bool
		wantErr   bool
	}{
		{name: "below gate", version: "v1.2.0"},
		{name: "gate is excluded", version: "v1.2.1"},
		{name: "gate prerelease is excluded", version: "v1.2.1-rc.1"},
		{name: "next patch prerelease is supported", version: "v1.2.2-rc.1", supported: true},
		{name: "current release is supported", version: "v1.3.0", supported: true},
		{name: "invalid version", version: "custom", wantErr: true},
		{name: "surrounding whitespace", version: " v1.3.0 ", wantErr: true},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			supported, err := SupportsRouterClientConfig(testCase.version)
			if testCase.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, testCase.supported, supported)
		})
	}
}
