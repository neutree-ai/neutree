package releaseprofile

import (
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestValidateClusterVersionCompatibility(t *testing.T) {
	info := &v1.ReleaseInfo{Spec: &v1.ReleaseInfoSpec{
		DefaultClusterVersion:      "v1.2.0",
		CompatibleClusterBaselines: []string{"v1.1", "v1.2"},
	}}

	tests := []struct {
		name    string
		version string
		wantErr string
	}{
		{name: "compatible historical patch", version: "v1.1.1"},
		{name: "compatible prerelease", version: "v1.2.0-alpha.1"},
		{name: "default version", version: "v1.2.0"},
		{name: "above default", version: "v1.2.1", wantErr: "exceeds default cluster version"},
		{name: "incompatible minor", version: "v1.0.1", wantErr: "incompatible baseline"},
		{name: "invalid complete version", version: "v1.2", wantErr: "invalid cluster version"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateClusterVersionCompatibility(info, tt.version)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}

			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}
