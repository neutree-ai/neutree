package version

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSupportsKubeStateMetrics(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    bool
		wantErr bool
	}{
		{name: "empty", version: "", want: false},
		{name: "before minimum", version: "v1.0.0", want: false},
		{name: "minimum", version: "v1.1.0", want: true},
		{name: "minimum nightly", version: "v1.1.0-nightly-20260418", want: true},
		{name: "invalid", version: "invalid", wantErr: true},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := SupportsKubeStateMetrics(testCase.version)
			if testCase.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, testCase.want, got)
		})
	}
}
