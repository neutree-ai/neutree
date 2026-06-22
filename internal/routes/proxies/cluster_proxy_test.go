package proxies

import (
	"errors"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

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

func TestValidateClusterAcceleratorVirtualizationDisable(t *testing.T) {
	vGPUEndpoint := v1.Endpoint{
		Spec: &v1.EndpointSpec{
			Resources: &v1.ResourceSpec{
				Accelerator: map[string]string{
					v1.AcceleratorVirtualizationMemoryMiBKey: "8192",
				},
			},
		},
	}
	nonVGPUEndpoint := v1.Endpoint{
		Spec: &v1.EndpointSpec{
			Resources: &v1.ResourceSpec{
				Accelerator: map[string]string{
					v1.AcceleratorTypeKey: "nvidia_gpu",
				},
			},
		},
	}

	t.Run("rejects disabling when vGPU endpoint references cluster", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		cluster := v1.Cluster{
			Metadata: &v1.Metadata{Workspace: "default", Name: "gpu-cluster"},
			Spec: &v1.ClusterSpec{
				AcceleratorVirtualization: &v1.AcceleratorVirtualizationSpec{Enabled: false},
			},
		}
		expectedEndpointFilters := clusterEndpointReferenceFilters("default", "gpu-cluster")

		mockStorage.On("ListEndpoint", storage.ListOption{Filters: expectedEndpointFilters}).
			Return([]v1.Endpoint{vGPUEndpoint}, nil)

		err := validateClusterAcceleratorVirtualizationDisable(mockStorage, cluster, nil)

		assert.NotNil(t, err)
		assert.Equal(t, "10211", err.Code)
		assert.Contains(t, err.Message, "cannot disable accelerator virtualization")
		assert.Contains(t, err.Hint, "1 vGPU endpoint(s) still reference this cluster")
		mockStorage.AssertExpectations(t)
	})

	t.Run("allows disabling when only non-vGPU endpoint references cluster", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		cluster := v1.Cluster{
			Metadata: &v1.Metadata{Workspace: "default", Name: "gpu-cluster"},
			Spec: &v1.ClusterSpec{
				AcceleratorVirtualization: &v1.AcceleratorVirtualizationSpec{Enabled: false},
			},
		}
		expectedEndpointFilters := clusterEndpointReferenceFilters("default", "gpu-cluster")

		mockStorage.On("ListEndpoint", storage.ListOption{Filters: expectedEndpointFilters}).
			Return([]v1.Endpoint{nonVGPUEndpoint}, nil)

		err := validateClusterAcceleratorVirtualizationDisable(mockStorage, cluster, nil)

		assert.Nil(t, err)
		mockStorage.AssertExpectations(t)
	})

	t.Run("resolves cluster identity from patch query filters", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		clusterPatch := v1.Cluster{
			Spec: &v1.ClusterSpec{
				AcceleratorVirtualization: &v1.AcceleratorVirtualizationSpec{Enabled: false},
			},
		}
		query := url.Values{
			"metadata->>workspace": []string{"eq.default"},
			"metadata->>name":      []string{"eq.gpu-cluster"},
		}
		expectedEndpointFilters := clusterEndpointReferenceFilters("default", "gpu-cluster")

		mockStorage.On("ListCluster", mock.MatchedBy(func(opt storage.ListOption) bool {
			return sameFilters(opt.Filters, queryParamsToFilters(query))
		})).Return([]v1.Cluster{
			{Metadata: &v1.Metadata{Workspace: "default", Name: "gpu-cluster"}},
		}, nil)
		mockStorage.On("ListEndpoint", storage.ListOption{Filters: expectedEndpointFilters}).
			Return([]v1.Endpoint{vGPUEndpoint}, nil)

		err := validateClusterAcceleratorVirtualizationDisable(mockStorage, clusterPatch, query)

		assert.NotNil(t, err)
		assert.Equal(t, "10211", err.Code)
		assert.Contains(t, err.Hint, "1 vGPU endpoint(s) still reference this cluster")
		mockStorage.AssertExpectations(t)
	})

	t.Run("returns validation error when endpoint lookup fails", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		cluster := v1.Cluster{
			Metadata: &v1.Metadata{Workspace: "default", Name: "gpu-cluster"},
			Spec: &v1.ClusterSpec{
				AcceleratorVirtualization: &v1.AcceleratorVirtualizationSpec{Enabled: false},
			},
		}
		expectedEndpointFilters := clusterEndpointReferenceFilters("default", "gpu-cluster")

		mockStorage.On("ListEndpoint", storage.ListOption{Filters: expectedEndpointFilters}).
			Return(nil, errors.New("database error"))

		err := validateClusterAcceleratorVirtualizationDisable(mockStorage, cluster, nil)

		assert.NotNil(t, err)
		assert.Equal(t, "10209", err.Code)
		assert.Contains(t, err.Hint, "database error")
		mockStorage.AssertExpectations(t)
	})
}

func TestClusterAcceleratorVirtualizationDisableRequested(t *testing.T) {
	t.Run("true when payload explicitly sets enabled false", func(t *testing.T) {
		requested, err := clusterAcceleratorVirtualizationDisableRequested([]byte(`{
			"spec": {
				"accelerator_virtualization": {"enabled": false}
			}
		}`))

		assert.NoError(t, err)
		assert.True(t, requested)
	})

	t.Run("true when enabled is omitted from accelerator virtualization patch", func(t *testing.T) {
		requested, err := clusterAcceleratorVirtualizationDisableRequested([]byte(`{
			"spec": {
				"accelerator_virtualization": {"config_patch": {"devicePlugin": {}}}
			}
		}`))

		assert.NoError(t, err)
		assert.True(t, requested)
	})

	t.Run("true when accelerator virtualization patch is empty after omitempty marshal", func(t *testing.T) {
		requested, err := clusterAcceleratorVirtualizationDisableRequested([]byte(`{
			"spec": {
				"accelerator_virtualization": {}
			}
		}`))

		assert.NoError(t, err)
		assert.True(t, requested)
	})
}

func sameFilters(actual, expected []storage.Filter) bool {
	if len(actual) != len(expected) {
		return false
	}

	unmatched := append([]storage.Filter(nil), actual...)
	for _, expectedFilter := range expected {
		matched := false
		for i, actualFilter := range unmatched {
			if actualFilter == expectedFilter {
				unmatched = append(unmatched[:i], unmatched[i+1:]...)
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	return true
}
