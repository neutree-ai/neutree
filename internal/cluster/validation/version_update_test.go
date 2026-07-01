package validation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUsesStaticNodeClusterFlow(t *testing.T) {
	tests := []struct {
		name        string
		clusterType string
		version     string
		want        bool
		wantErr     bool
	}{
		{
			name:        "SSH cluster at gate stays on legacy flow",
			clusterType: "ssh",
			version:     "v1.0.1",
		},
		{
			name:        "SSH cluster above gate uses static flow",
			clusterType: "ssh",
			version:     "v1.0.2",
			want:        true,
		},
		{
			name:        "Kubernetes cluster does not use static flow",
			clusterType: "kubernetes",
			version:     "v1.1.0",
		},
		{
			name:        "invalid SSH cluster version returns error",
			clusterType: "ssh",
			version:     "custom",
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := UsesStaticNodeClusterFlow(tt.clusterType, tt.version)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestValidateStaticNodeClusterFlowVersionUpdate(t *testing.T) {
	tests := []struct {
		name            string
		clusterType     string
		previousVersion string
		desiredVersion  string
		reason          ClusterVersionUpdateErrorReason
	}{
		{
			name:            "allows legacy to static upgrade",
			clusterType:     "ssh",
			previousVersion: "v1.0.1",
			desiredVersion:  "v1.1.0",
		},
		{
			name:            "allows static flow version update",
			clusterType:     "ssh",
			previousVersion: "v1.1.0",
			desiredVersion:  "v1.0.2",
		},
		{
			name:            "rejects static flow downgrade to legacy flow",
			clusterType:     "ssh",
			previousVersion: "v1.1.0",
			desiredVersion:  "v1.0.1",
			reason:          ClusterVersionUpdateUnsupportedDowngradeReason,
		},
		{
			name:            "allows Kubernetes version downgrade",
			clusterType:     "kubernetes",
			previousVersion: "v1.1.0",
			desiredVersion:  "v1.0.1",
		},
		{
			name:            "rejects invalid desired SSH version",
			clusterType:     "ssh",
			previousVersion: "v1.1.0",
			desiredVersion:  "custom",
			reason:          ClusterVersionUpdateInvalidVersionReason,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateStaticNodeClusterFlowVersionUpdate(tt.clusterType, tt.previousVersion, tt.desiredVersion)
			if tt.reason == "" {
				require.NoError(t, err)
				return
			}

			require.Error(t, err)

			var validationErr *ClusterVersionUpdateError
			require.ErrorAs(t, err, &validationErr)
			assert.Equal(t, tt.reason, validationErr.Reason)
		})
	}
}
