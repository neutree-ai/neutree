package proxies

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	v1 "github.com/neutree-ai/neutree/api/v1"
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
		err := validateClusterAcceleratorVirtualizationBody(http.MethodPost, []byte(`{
			"spec": {
				"type": "kubernetes",
				"version": "v1.1.0",
				"accelerator_virtualization": {
					"enabled": true,
					"config_patch": {"devicePlugin": {"nvidiaDriverRoot": "/run/nvidia/driver"}}
				}
			}
		}`), "", nil)

		assert.Nil(t, err)
	})

	t.Run("allows Kubernetes nightly cluster with minimum base version to enable accelerator virtualization", func(t *testing.T) {
		err := validateClusterAcceleratorVirtualizationBody(http.MethodPost, []byte(`{
			"spec": {
				"type": "kubernetes",
				"version": "v1.1.0-nightly-20260603",
				"accelerator_virtualization": {"enabled": true}
			}
		}`), "", nil)

		assert.Nil(t, err)
	})

	t.Run("rejects Kubernetes cluster below minimum version enabling accelerator virtualization", func(t *testing.T) {
		err := validateClusterAcceleratorVirtualizationBody(http.MethodPost, []byte(`{
			"spec": {
				"type": "kubernetes",
				"version": "v1.0.9",
				"accelerator_virtualization": {"enabled": true}
			}
		}`), "", nil)

		assert.NotNil(t, err)
		assert.Equal(t, "10208", err.Code)
		assert.Contains(t, err.Message, "requires cluster version >= v1.1.0")
	})

	t.Run("rejects Kubernetes cluster missing version enabling accelerator virtualization", func(t *testing.T) {
		err := validateClusterAcceleratorVirtualizationBody(http.MethodPost, []byte(`{
			"spec": {
				"type": "kubernetes",
				"accelerator_virtualization": {"enabled": true}
			}
		}`), "", nil)

		assert.NotNil(t, err)
		assert.Equal(t, "10208", err.Code)
		assert.Contains(t, err.Message, "requires cluster version >= v1.1.0")
	})

	t.Run("rejects invalid cluster version enabling accelerator virtualization", func(t *testing.T) {
		err := validateClusterAcceleratorVirtualizationBody(http.MethodPost, []byte(`{
			"spec": {
				"type": "kubernetes",
				"version": "nightly",
				"accelerator_virtualization": {"enabled": true}
			}
		}`), "", nil)

		assert.NotNil(t, err)
		assert.Equal(t, "10209", err.Code)
		assert.Equal(t, "invalid cluster version", err.Message)
	})

	t.Run("rejects SSH cluster enabling accelerator virtualization", func(t *testing.T) {
		err := validateClusterAcceleratorVirtualizationBody(http.MethodPost, []byte(`{
			"spec": {
				"type": "ssh",
				"accelerator_virtualization": {"enabled": true}
			}
		}`), "", nil)

		assert.NotNil(t, err)
		assert.Equal(t, "10208", err.Code)
	})

	t.Run("rejects non-bool enabled", func(t *testing.T) {
		err := validateClusterAcceleratorVirtualizationBody(http.MethodPost, []byte(`{
			"spec": {
				"type": "kubernetes",
				"version": "v1.1.0",
				"accelerator_virtualization": {"enabled": "true"}
			}
		}`), "", nil)

		assert.NotNil(t, err)
		assert.Equal(t, "10210", err.Code)
	})

	t.Run("rejects non-object config_patch", func(t *testing.T) {
		err := validateClusterAcceleratorVirtualizationBody(http.MethodPost, []byte(`{
			"spec": {
				"type": "kubernetes",
				"version": "v1.1.0",
				"accelerator_virtualization": {"enabled": true, "config_patch": ["invalid"]}
			}
		}`), "", nil)

		assert.NotNil(t, err)
		assert.Equal(t, "10209", err.Code)
	})

	t.Run("loads existing cluster type for partial patch", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		mockStorage.On("ListCluster", storage.ListOption{
			Filters: []storage.Filter{
				{Column: "metadata->>name", Operator: "eq", Value: "cluster"},
				{Column: "metadata->>workspace", Operator: "eq", Value: "default"},
			},
		}).Return([]v1.Cluster{
			{
				Spec: &v1.ClusterSpec{Type: v1.SSHClusterType, Version: "v1.1.0"},
			},
		}, nil)

		err := validateClusterAcceleratorVirtualizationBody(http.MethodPatch, []byte(`{
			"spec": {
				"accelerator_virtualization": {"enabled": true}
			}
		}`), "metadata->>name=eq.cluster&metadata->>workspace=eq.default", mockStorage)

		assert.NotNil(t, err)
		assert.Equal(t, "10208", err.Code)
		mockStorage.AssertExpectations(t)
	})

	t.Run("loads existing cluster version for partial patch", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		mockStorage.On("ListCluster", storage.ListOption{
			Filters: []storage.Filter{
				{Column: "metadata->>name", Operator: "eq", Value: "cluster"},
				{Column: "metadata->>workspace", Operator: "eq", Value: "default"},
			},
		}).Return([]v1.Cluster{
			{
				Spec: &v1.ClusterSpec{Type: v1.KubernetesClusterType, Version: "v1.0.9"},
			},
		}, nil)

		err := validateClusterAcceleratorVirtualizationBody(http.MethodPatch, []byte(`{
			"spec": {
				"accelerator_virtualization": {"enabled": true}
			}
		}`), "metadata->>name=eq.cluster&metadata->>workspace=eq.default", mockStorage)

		assert.NotNil(t, err)
		assert.Equal(t, "10208", err.Code)
		assert.Contains(t, err.Message, "requires cluster version >= v1.1.0")
		mockStorage.AssertExpectations(t)
	})

	t.Run("allows partial patch when existing cluster version is supported", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		mockStorage.On("ListCluster", storage.ListOption{
			Filters: []storage.Filter{
				{Column: "metadata->>name", Operator: "eq", Value: "cluster"},
				{Column: "metadata->>workspace", Operator: "eq", Value: "default"},
			},
		}).Return([]v1.Cluster{
			{
				Spec: &v1.ClusterSpec{Type: v1.KubernetesClusterType, Version: "v1.1.0"},
			},
		}, nil)

		err := validateClusterAcceleratorVirtualizationBody(http.MethodPatch, []byte(`{
			"spec": {
				"accelerator_virtualization": {"enabled": true}
			}
		}`), "metadata->>name=eq.cluster&metadata->>workspace=eq.default", mockStorage)

		assert.Nil(t, err)
		mockStorage.AssertExpectations(t)
	})
}
