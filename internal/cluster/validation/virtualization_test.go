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

func TestValidateAcceleratorVirtualizationClusterSupport(t *testing.T) {
	tests := []struct {
		name    string
		typ     string
		version string
		reason  AcceleratorVirtualizationErrorReason
	}{
		{
			name:    "allows supported Kubernetes cluster",
			typ:     "kubernetes",
			version: "v1.1.0",
		},
		{
			name:    "rejects SSH cluster",
			typ:     "ssh",
			version: "v1.1.0",
			reason:  AcceleratorVirtualizationUnsupportedClusterReason,
		},
		{
			name:    "rejects older cluster version",
			typ:     "kubernetes",
			version: "v1.0.9",
			reason:  AcceleratorVirtualizationUnsupportedVersionReason,
		},
		{
			name:    "rejects invalid cluster version",
			typ:     "kubernetes",
			version: "nightly",
			reason:  AcceleratorVirtualizationInvalidVersionReason,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAcceleratorVirtualizationClusterSupport(tt.typ, tt.version)
			if tt.reason == "" {
				require.NoError(t, err)
				return
			}

			require.Error(t, err)

			var validationErr *AcceleratorVirtualizationError
			require.ErrorAs(t, err, &validationErr)
			assert.Equal(t, tt.reason, validationErr.Reason)
		})
	}
}

func TestValidateAcceleratorVirtualizationConfigPatch(t *testing.T) {
	tests := []struct {
		name        string
		configPatch map[string]interface{}
		reason      AcceleratorVirtualizationErrorReason
	}{
		{
			name: "allows supported config patch",
			configPatch: map[string]interface{}{
				"devicePlugin": map[string]interface{}{"nvidiaDriverRoot": "/run/nvidia/driver"},
				"scheduler":    map[string]interface{}{"defaultSchedulerPolicy": map[string]interface{}{}},
				"global":       map[string]interface{}{"imageRegistry": "registry.example.com"},
			},
		},
		{
			name:        "rejects unsupported top-level key",
			configPatch: map[string]interface{}{"dra": map[string]interface{}{"enabled": true}},
			reason:      AcceleratorVirtualizationUnsupportedConfigReason,
		},
		{
			name: "rejects scheduler patch hook",
			configPatch: map[string]interface{}{
				"scheduler": map[string]interface{}{"patch": map[string]interface{}{"enabled": true}},
			},
			reason: AcceleratorVirtualizationManagedSchedulerReason,
		},
		{
			name: "rejects cert-manager integration",
			configPatch: map[string]interface{}{
				"scheduler": map[string]interface{}{"certManager": map[string]interface{}{"enabled": true}},
			},
			reason: AcceleratorVirtualizationManagedCertManagerReason,
		},
		{
			name: "rejects MIG mode",
			configPatch: map[string]interface{}{
				"devicePlugin": map[string]interface{}{"migStrategy": "mixed"},
			},
			reason: AcceleratorVirtualizationUnsupportedMIGReason,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAcceleratorVirtualizationConfigPatch(tt.configPatch)
			if tt.reason == "" {
				require.NoError(t, err)
				return
			}

			require.Error(t, err)

			var validationErr *AcceleratorVirtualizationError
			require.ErrorAs(t, err, &validationErr)
			assert.Equal(t, tt.reason, validationErr.Reason)
		})
	}
}
