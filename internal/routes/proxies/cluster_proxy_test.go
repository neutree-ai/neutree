package proxies

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/pkg/storage"
	storageMocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func TestValidateClusterDeletion(t *testing.T) {
	tests := []struct {
		name          string
		workspace     string
		clusterName   string
		endpointCount int
		queryError    error
		expectError   bool
		expectedCode  string
		expectedHint  string
	}{
		{
			name:          "no dependencies - deletion allowed",
			workspace:     "default",
			clusterName:   "my-cluster",
			endpointCount: 0,
			queryError:    nil,
			expectError:   false,
		},
		{
			name:          "has dependencies - deletion blocked",
			workspace:     "default",
			clusterName:   "my-cluster",
			endpointCount: 5,
			queryError:    nil,
			expectError:   true,
			expectedCode:  "10126",
			expectedHint:  "5 endpoint(s) still reference this cluster",
		},
		{
			name:          "query error",
			workspace:     "default",
			clusterName:   "my-cluster",
			endpointCount: 0,
			queryError:    errors.New("database error"),
			expectError:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := storageMocks.NewMockStorage(t)

			mockStorage.On("Count",
				storage.ENDPOINT_TABLE,
				[]storage.Filter{
					{Column: "metadata->>workspace", Operator: "eq", Value: tt.workspace},
					{Column: "spec->>cluster", Operator: "eq", Value: tt.clusterName},
				},
			).Return(tt.endpointCount, tt.queryError)

			validator := validateClusterDeletion(mockStorage)
			err := validator(tt.workspace, tt.clusterName)

			if tt.expectError {
				assert.Error(t, err)

				if tt.queryError == nil {
					deletionErr, ok := err.(*middleware.DeletionError)
					assert.True(t, ok, "error should be DeletionError")
					if ok {
						assert.Equal(t, tt.expectedCode, deletionErr.Code)
						assert.Contains(t, deletionErr.Hint, tt.expectedHint)
					}
				}
			} else {
				assert.NoError(t, err)
			}

			mockStorage.AssertExpectations(t)
		})
	}
}

func TestValidateClusterAcceleratorVirtualizationBody(t *testing.T) {
	t.Run("allows Kubernetes cluster to enable accelerator virtualization", func(t *testing.T) {
		err := validateClusterAcceleratorVirtualizationBody([]byte(`{
			"spec": {
				"type": "kubernetes",
				"version": "v1.1.0",
				"accelerator_virtualization": {
					"enabled": true,
					"config_patch": {"devicePlugin": {"nvidiaDriverRoot": "/run/nvidia/driver"}}
				}
			}
		}`))

		assert.Nil(t, err)
	})

	t.Run("allows Kubernetes nightly cluster with minimum base version to enable accelerator virtualization", func(t *testing.T) {
		err := validateClusterAcceleratorVirtualizationBody([]byte(`{
			"spec": {
				"type": "kubernetes",
				"version": "v1.1.0-nightly-20260603",
				"accelerator_virtualization": {"enabled": true}
			}
		}`))

		assert.Nil(t, err)
	})

	t.Run("rejects Kubernetes cluster below minimum version enabling accelerator virtualization", func(t *testing.T) {
		err := validateClusterAcceleratorVirtualizationBody([]byte(`{
			"spec": {
				"type": "kubernetes",
				"version": "v1.0.9",
				"accelerator_virtualization": {"enabled": true}
			}
		}`))

		assert.NotNil(t, err)
		assert.Equal(t, "10208", err.Code)
		assert.Contains(t, err.Message, "requires cluster version >= v1.1.0")
	})

	t.Run("rejects Kubernetes cluster missing version enabling accelerator virtualization", func(t *testing.T) {
		err := validateClusterAcceleratorVirtualizationBody([]byte(`{
			"spec": {
				"type": "kubernetes",
				"accelerator_virtualization": {"enabled": true}
			}
		}`))

		assert.NotNil(t, err)
		assert.Equal(t, "10208", err.Code)
		assert.Contains(t, err.Message, "requires cluster version >= v1.1.0")
	})

	t.Run("rejects invalid cluster version enabling accelerator virtualization", func(t *testing.T) {
		err := validateClusterAcceleratorVirtualizationBody([]byte(`{
			"spec": {
				"type": "kubernetes",
				"version": "nightly",
				"accelerator_virtualization": {"enabled": true}
			}
		}`))

		assert.NotNil(t, err)
		assert.Equal(t, "10209", err.Code)
		assert.Equal(t, "invalid cluster version", err.Message)
	})

	t.Run("rejects SSH cluster enabling accelerator virtualization", func(t *testing.T) {
		err := validateClusterAcceleratorVirtualizationBody([]byte(`{
			"spec": {
				"type": "ssh",
				"accelerator_virtualization": {"enabled": true}
			}
		}`))

		assert.NotNil(t, err)
		assert.Equal(t, "10208", err.Code)
	})

	t.Run("rejects non-bool enabled", func(t *testing.T) {
		err := validateClusterAcceleratorVirtualizationBody([]byte(`{
			"spec": {
				"type": "kubernetes",
				"version": "v1.1.0",
				"accelerator_virtualization": {"enabled": "true"}
			}
		}`))

		assert.NotNil(t, err)
		assert.Equal(t, "10209", err.Code)
		assert.Equal(t, "invalid cluster payload", err.Message)
	})

	t.Run("rejects non-object config_patch", func(t *testing.T) {
		err := validateClusterAcceleratorVirtualizationBody([]byte(`{
			"spec": {
				"type": "kubernetes",
				"version": "v1.1.0",
				"accelerator_virtualization": {"enabled": true, "config_patch": ["invalid"]}
			}
		}`))

		assert.NotNil(t, err)
		assert.Equal(t, "10209", err.Code)
		assert.Equal(t, "invalid cluster payload", err.Message)
	})

	t.Run("skips accelerator virtualization validation for soft delete patch", func(t *testing.T) {
		err := validateClusterAcceleratorVirtualizationBody([]byte(`{
			"metadata": {
				"name": "cluster",
				"workspace": "default",
				"deletion_timestamp": "2026-06-10T00:00:00Z"
			},
			"spec": {
				"type": "ssh",
				"accelerator_virtualization": {"enabled": true}
			}
		}`))

		assert.Nil(t, err)
	})

	t.Run("rejects unsupported config patch key", func(t *testing.T) {
		err := validateClusterAcceleratorVirtualizationBody([]byte(`{
			"spec": {
				"type": "kubernetes",
				"version": "v1.1.0",
				"accelerator_virtualization": {
					"enabled": true,
					"config_patch": {"dra": {"enabled": true}}
				}
			}
		}`))

		assert.NotNil(t, err)
		assert.Equal(t, "10210", err.Code)
		assert.Contains(t, err.Message, "unsupported")
	})

	t.Run("rejects MIG virtualization config patch", func(t *testing.T) {
		err := validateClusterAcceleratorVirtualizationBody([]byte(`{
			"spec": {
				"type": "kubernetes",
				"version": "v1.1.0",
				"accelerator_virtualization": {
					"enabled": true,
					"config_patch": {"devicePlugin": {"migStrategy": "mixed"}}
				}
			}
		}`))

		assert.NotNil(t, err)
		assert.Equal(t, "10210", err.Code)
		assert.Contains(t, err.Message, "MIG")
	})

	t.Run("rejects partial patch missing cluster type and version", func(t *testing.T) {
		err := validateClusterAcceleratorVirtualizationBody([]byte(`{
			"metadata": {"name": "cluster", "workspace": "default"},
			"spec": {
				"accelerator_virtualization": {"enabled": true}
			}
		}`))

		assert.NotNil(t, err)
		assert.Equal(t, "10208", err.Code)
		assert.Contains(t, err.Message, "only supported for Kubernetes")
	})

	t.Run("rejects partial patch missing cluster version", func(t *testing.T) {
		err := validateClusterAcceleratorVirtualizationBody([]byte(`{
			"metadata": {"name": "cluster", "workspace": "default"},
			"spec": {
				"type": "kubernetes",
				"accelerator_virtualization": {"enabled": true}
			}
		}`))

		assert.NotNil(t, err)
		assert.Equal(t, "10208", err.Code)
		assert.Contains(t, err.Message, "requires cluster version >= v1.1.0")
	})
}
