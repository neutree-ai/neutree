package proxies

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
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

	t.Run("rejects mismatched patch body identity and query target", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		clusterPatch := v1.Cluster{
			Metadata: &v1.Metadata{Workspace: "default", Name: "body-cluster"},
			Spec: &v1.ClusterSpec{
				AcceleratorVirtualization: &v1.AcceleratorVirtualizationSpec{Enabled: false},
			},
		}
		query := url.Values{
			"id": []string{"eq.1"},
		}

		mockStorage.On("ListCluster", mock.MatchedBy(func(opt storage.ListOption) bool {
			return sameFilters(opt.Filters, queryParamsToFilters(query))
		})).Return([]v1.Cluster{
			{Metadata: &v1.Metadata{Workspace: "default", Name: "query-cluster"}},
		}, nil)

		err := validateClusterAcceleratorVirtualizationDisable(mockStorage, clusterPatch, query)

		if assert.NotNil(t, err) {
			assert.Equal(t, "10209", err.Code)
			assert.Contains(t, err.Hint, "does not match patch target")
		}
		mockStorage.AssertExpectations(t)
		mockStorage.AssertNotCalled(t, "ListEndpoint", mock.Anything)
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

	t.Run("rejects clearing accelerator virtualization with null while vGPU endpoint references cluster", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		cluster := v1.Cluster{
			Metadata: &v1.Metadata{Workspace: "default", Name: "gpu-cluster"},
			Spec:     &v1.ClusterSpec{AcceleratorVirtualization: nil},
		}
		expectedEndpointFilters := clusterEndpointReferenceFilters("default", "gpu-cluster")

		mockStorage.On("ListEndpoint", storage.ListOption{Filters: expectedEndpointFilters}).
			Return([]v1.Endpoint{vGPUEndpoint}, nil)

		err := validateClusterAcceleratorVirtualizationDisable(mockStorage, cluster, nil)

		if assert.NotNil(t, err) {
			assert.Equal(t, "10211", err.Code)
			assert.Contains(t, err.Hint, "1 vGPU endpoint(s) still reference this cluster")
		}
		mockStorage.AssertExpectations(t)
	})
}

func TestValidateClusterAcceleratorVirtualizationMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("rejects disable patch before proxy handler when vGPU endpoint references cluster", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		mockStorage.On("ListEndpoint", storage.ListOption{
			Filters: clusterEndpointReferenceFilters("default", "gpu-cluster"),
		}).Return([]v1.Endpoint{
			{
				Spec: &v1.EndpointSpec{
					Resources: &v1.ResourceSpec{
						Accelerator: map[string]string{
							v1.AcceleratorVirtualizationMemoryMiBKey: "8192",
						},
					},
				},
			},
		}, nil)

		proxyCalled := false
		router := gin.New()
		router.PATCH("/clusters", validateClusterAcceleratorVirtualization(mockStorage), func(c *gin.Context) {
			proxyCalled = true
			c.Status(http.StatusNoContent)
		})

		body := `{
			"metadata": {"workspace": "default", "name": "gpu-cluster"},
			"spec": {"accelerator_virtualization": {"enabled": false}}
		}`
		req := httptest.NewRequest(http.MethodPatch, "/clusters", strings.NewReader(body))
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, req)

		assert.False(t, proxyCalled)
		assert.Equal(t, http.StatusBadRequest, recorder.Code)
		assert.Contains(t, recorder.Body.String(), `"code":"10211"`)
		assert.Contains(t, recorder.Body.String(), "vGPU endpoint(s) still reference this cluster")
		mockStorage.AssertExpectations(t)
	})

	t.Run("allows non-disable patch to continue to proxy handler", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		proxyCalled := false
		router := gin.New()
		router.PATCH("/clusters", validateClusterAcceleratorVirtualization(mockStorage), func(c *gin.Context) {
			proxyCalled = true
			c.Status(http.StatusNoContent)
		})

		body := `{"metadata": {"workspace": "default", "name": "gpu-cluster"}}`
		req := httptest.NewRequest(http.MethodPatch, "/clusters", strings.NewReader(body))
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, req)

		assert.True(t, proxyCalled)
		assert.Equal(t, http.StatusNoContent, recorder.Code)
		mockStorage.AssertNotCalled(t, "ListEndpoint", mock.Anything)
	})

	t.Run("allows soft delete without validating guarded configuration", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		proxyCalled := false
		router := gin.New()
		router.PATCH("/clusters", validateClusterAcceleratorVirtualization(mockStorage), func(c *gin.Context) {
			proxyCalled = true
			c.Status(http.StatusNoContent)
		})

		req := httptest.NewRequest(http.MethodPatch, "/clusters", strings.NewReader(`{
			"metadata": {"deletion_timestamp": "2026-08-10T00:00:00Z"},
			"spec": {"accelerator_virtualization": {"enabled": false}}
		}`))
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, req)

		assert.True(t, proxyCalled)
		assert.Equal(t, http.StatusNoContent, recorder.Code)
		mockStorage.AssertNotCalled(t, "ListEndpoint", mock.Anything)
	})
}

func TestValidateClusterVersionUpdateMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("rejects image registry switch for initialized cluster before proxy handler", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		query := url.Values{"id": []string{"eq.1"}}
		mockStorage.On("ListCluster", mock.MatchedBy(func(opt storage.ListOption) bool {
			return sameFilters(opt.Filters, queryParamsToFilters(query))
		})).Return([]v1.Cluster{
			{
				Spec:   &v1.ClusterSpec{ImageRegistry: "current-registry"},
				Status: &v1.ClusterStatus{Initialized: true},
			},
		}, nil)

		proxyCalled := false
		router := gin.New()
		router.PATCH("/clusters", validateClusterVersionUpdate(mockStorage), func(c *gin.Context) {
			proxyCalled = true
			c.Status(http.StatusNoContent)
		})

		req := httptest.NewRequest(http.MethodPatch, "/clusters?id=eq.1", strings.NewReader(`{
			"spec": {"image_registry": "replacement-registry"}
		}`))
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, req)

		assert.False(t, proxyCalled)
		assert.Equal(t, http.StatusBadRequest, recorder.Code)
		assert.Contains(t, recorder.Body.String(), `"code":"10209"`)
		assert.Contains(t, recorder.Body.String(), "image registry")
		mockStorage.AssertExpectations(t)
	})

	t.Run("allows kubeconfig rotation for the same Kubernetes API server", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		query := url.Values{"id": []string{"eq.1"}}
		mockStorage.On("ListCluster", mock.MatchedBy(func(opt storage.ListOption) bool {
			return sameFilters(opt.Filters, queryParamsToFilters(query))
		})).Return([]v1.Cluster{{
			Spec: &v1.ClusterSpec{
				Type: v1.KubernetesClusterType,
				Config: &v1.ClusterConfig{KubernetesConfig: &v1.KubernetesClusterConfig{
					Kubeconfig: testEncodedKubeconfig("https://api.example.test:6443", "old-token"),
				}},
			},
			Status: &v1.ClusterStatus{Initialized: true},
		}}, nil).Once()

		proxyCalled := false
		router := gin.New()
		router.PATCH("/clusters", validateClusterVersionUpdate(mockStorage), func(c *gin.Context) {
			proxyCalled = true
			c.Status(http.StatusNoContent)
		})

		body := fmt.Sprintf(`{"spec":{"config":{"kubernetes_config":{"kubeconfig":%q}}}}`,
			testEncodedKubeconfig("https://api.example.test:6443", "rotated-token"))
		req := httptest.NewRequest(http.MethodPatch, "/clusters?id=eq.1", strings.NewReader(body))
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, req)

		assert.True(t, proxyCalled)
		assert.Equal(t, http.StatusNoContent, recorder.Code)
		mockStorage.AssertExpectations(t)
	})

	t.Run("rejects kubeconfig rotation for a different Kubernetes API server", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		query := url.Values{"id": []string{"eq.1"}}
		mockStorage.On("ListCluster", mock.MatchedBy(func(opt storage.ListOption) bool {
			return sameFilters(opt.Filters, queryParamsToFilters(query))
		})).Return([]v1.Cluster{{
			Spec: &v1.ClusterSpec{
				Type: v1.KubernetesClusterType,
				Config: &v1.ClusterConfig{KubernetesConfig: &v1.KubernetesClusterConfig{
					Kubeconfig: testEncodedKubeconfig("https://api.example.test:6443", "old-token"),
				}},
			},
			Status: &v1.ClusterStatus{Initialized: true},
		}}, nil)

		proxyCalled := false
		router := gin.New()
		router.PATCH("/clusters", validateClusterVersionUpdate(mockStorage), func(c *gin.Context) {
			proxyCalled = true
			c.Status(http.StatusNoContent)
		})

		body := fmt.Sprintf(`{"spec":{"config":{"kubernetes_config":{"kubeconfig":%q}}}}`,
			testEncodedKubeconfig("https://other-api.example.test:6443", "rotated-token"))
		req := httptest.NewRequest(http.MethodPatch, "/clusters?id=eq.1", strings.NewReader(body))
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, req)

		assert.False(t, proxyCalled)
		assert.Equal(t, http.StatusBadRequest, recorder.Code)
		assert.Contains(t, recorder.Body.String(), `"code":"10209"`)
		assert.Contains(t, recorder.Body.String(), "current Kubernetes API server")
		mockStorage.AssertExpectations(t)
	})

	t.Run("rejects clearing config on an initialized Kubernetes cluster", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		query := url.Values{"id": []string{"eq.1"}}
		mockStorage.On("ListCluster", mock.MatchedBy(func(opt storage.ListOption) bool {
			return sameFilters(opt.Filters, queryParamsToFilters(query))
		})).Return([]v1.Cluster{{
			Spec: &v1.ClusterSpec{
				Type: v1.KubernetesClusterType,
				Config: &v1.ClusterConfig{KubernetesConfig: &v1.KubernetesClusterConfig{
					Kubeconfig: testEncodedKubeconfig("https://api.example.test:6443", "old-token"),
				}},
			},
			Status: &v1.ClusterStatus{Initialized: true},
		}}, nil)

		proxyCalled := false
		router := gin.New()
		router.PATCH("/clusters", validateClusterVersionUpdate(mockStorage), func(c *gin.Context) {
			proxyCalled = true
			c.Status(http.StatusNoContent)
		})

		req := httptest.NewRequest(http.MethodPatch, "/clusters?id=eq.1", strings.NewReader(`{
			"spec": {"config": null}
		}`))
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, req)

		assert.False(t, proxyCalled)
		assert.Equal(t, http.StatusBadRequest, recorder.Code)
		assert.Contains(t, recorder.Body.String(), `"code":"10209"`)
		assert.Contains(t, recorder.Body.String(), "config cannot be cleared")
		mockStorage.AssertExpectations(t)
	})

	t.Run("allows SSH private key rotation for an initialized cluster", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		query := url.Values{"id": []string{"eq.1"}}
		mockStorage.On("ListCluster", mock.MatchedBy(func(opt storage.ListOption) bool {
			return sameFilters(opt.Filters, queryParamsToFilters(query))
		})).Return([]v1.Cluster{{
			Spec: &v1.ClusterSpec{
				Type: v1.SSHClusterType,
				Config: &v1.ClusterConfig{SSHConfig: &v1.RaySSHProvisionClusterConfig{
					Auth: v1.Auth{SSHPrivateKey: "old-private-key"},
				}},
			},
			Status: &v1.ClusterStatus{Initialized: true},
		}}, nil)

		proxyCalled := false
		router := gin.New()
		router.PATCH("/clusters", validateClusterVersionUpdate(mockStorage), func(c *gin.Context) {
			proxyCalled = true
			c.Status(http.StatusNoContent)
		})

		body := fmt.Sprintf(`{"spec":{"config":{"ssh_config":{"auth":{"ssh_private_key":%q}}}}}`,
			base64.StdEncoding.EncodeToString([]byte("rotated-private-key")))
		req := httptest.NewRequest(http.MethodPatch, "/clusters?id=eq.1", strings.NewReader(body))
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, req)

		assert.True(t, proxyCalled)
		assert.Equal(t, http.StatusNoContent, recorder.Code)
		mockStorage.AssertExpectations(t)
	})

	t.Run("rejects empty SSH private key for an initialized cluster", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		query := url.Values{"id": []string{"eq.1"}}
		mockStorage.On("ListCluster", mock.MatchedBy(func(opt storage.ListOption) bool {
			return sameFilters(opt.Filters, queryParamsToFilters(query))
		})).Return([]v1.Cluster{{
			Spec:   &v1.ClusterSpec{Type: v1.SSHClusterType},
			Status: &v1.ClusterStatus{Initialized: true},
		}}, nil)

		proxyCalled := false
		router := gin.New()
		router.PATCH("/clusters", validateClusterVersionUpdate(mockStorage), func(c *gin.Context) {
			proxyCalled = true
			c.Status(http.StatusNoContent)
		})

		req := httptest.NewRequest(http.MethodPatch, "/clusters?id=eq.1", strings.NewReader(`{
			"spec": {"config": {"ssh_config": {"auth": {"ssh_private_key": " "}}}}
		}`))
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, req)

		assert.False(t, proxyCalled)
		assert.Equal(t, http.StatusBadRequest, recorder.Code)
		assert.Contains(t, recorder.Body.String(), `"code":"10209"`)
		assert.Contains(t, recorder.Body.String(), "SSH private key cannot be empty")
		mockStorage.AssertExpectations(t)
	})

	t.Run("rejects malformed SSH private key for an initialized cluster", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		query := url.Values{"id": []string{"eq.1"}}
		mockStorage.On("ListCluster", mock.MatchedBy(func(opt storage.ListOption) bool {
			return sameFilters(opt.Filters, queryParamsToFilters(query))
		})).Return([]v1.Cluster{{
			Spec:   &v1.ClusterSpec{Type: v1.SSHClusterType},
			Status: &v1.ClusterStatus{Initialized: true},
		}}, nil)

		proxyCalled := false
		router := gin.New()
		router.PATCH("/clusters", validateClusterVersionUpdate(mockStorage), func(c *gin.Context) {
			proxyCalled = true
			c.Status(http.StatusNoContent)
		})

		req := httptest.NewRequest(http.MethodPatch, "/clusters?id=eq.1", strings.NewReader(`{
			"spec": {"config": {"ssh_config": {"auth": {"ssh_private_key": "invalid-base64"}}}}
		}`))
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, req)

		assert.False(t, proxyCalled)
		assert.Equal(t, http.StatusBadRequest, recorder.Code)
		assert.Contains(t, recorder.Body.String(), `"code":"10209"`)
		assert.Contains(t, recorder.Body.String(), "SSH private key must be base64 encoded")
		mockStorage.AssertExpectations(t)
	})

	t.Run("rejects clearing SSH auth on an initialized cluster", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		query := url.Values{"id": []string{"eq.1"}}
		mockStorage.On("ListCluster", mock.MatchedBy(func(opt storage.ListOption) bool {
			return sameFilters(opt.Filters, queryParamsToFilters(query))
		})).Return([]v1.Cluster{{
			Spec: &v1.ClusterSpec{
				Type: v1.SSHClusterType,
				Config: &v1.ClusterConfig{SSHConfig: &v1.RaySSHProvisionClusterConfig{
					Auth: v1.Auth{SSHPrivateKey: base64.StdEncoding.EncodeToString([]byte("old-private-key"))},
				}},
			},
			Status: &v1.ClusterStatus{Initialized: true},
		}}, nil)

		proxyCalled := false
		router := gin.New()
		router.PATCH("/clusters", validateClusterVersionUpdate(mockStorage), func(c *gin.Context) {
			proxyCalled = true
			c.Status(http.StatusNoContent)
		})

		req := httptest.NewRequest(http.MethodPatch, "/clusters?id=eq.1", strings.NewReader(`{
			"spec": {"config": {"ssh_config": {"auth": null}}}
		}`))
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, req)

		assert.False(t, proxyCalled)
		assert.Equal(t, http.StatusBadRequest, recorder.Code)
		assert.Contains(t, recorder.Body.String(), `"code":"10209"`)
		assert.Contains(t, recorder.Body.String(), "SSH auth cannot be cleared")
		mockStorage.AssertExpectations(t)
	})

	t.Run("allows image registry change before cluster initialization", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		query := url.Values{"id": []string{"eq.1"}}
		mockStorage.On("ListCluster", mock.MatchedBy(func(opt storage.ListOption) bool {
			return sameFilters(opt.Filters, queryParamsToFilters(query))
		})).Return([]v1.Cluster{{
			Spec:   &v1.ClusterSpec{ImageRegistry: "current-registry"},
			Status: &v1.ClusterStatus{Initialized: false},
		}}, nil)

		proxyCalled := false
		router := gin.New()
		router.PATCH("/clusters", validateClusterVersionUpdate(mockStorage), func(c *gin.Context) {
			proxyCalled = true
			c.Status(http.StatusNoContent)
		})

		req := httptest.NewRequest(http.MethodPatch, "/clusters?id=eq.1", strings.NewReader(`{
			"spec": {"image_registry": "replacement-registry"}
		}`))
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, req)

		assert.True(t, proxyCalled)
		assert.Equal(t, http.StatusNoContent, recorder.Code)
		mockStorage.AssertExpectations(t)
	})

	t.Run("skips malformed configuration validation for a soft delete patch", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		proxyCalled := false
		router := gin.New()
		router.PATCH("/clusters", validateClusterVersionUpdate(mockStorage), func(c *gin.Context) {
			proxyCalled = true
			c.Status(http.StatusNoContent)
		})

		req := httptest.NewRequest(http.MethodPatch, "/clusters?id=eq.1", strings.NewReader(`{
			"metadata": {"deletion_timestamp": "2026-08-07T00:00:00Z"},
			"spec": {"config": {"kubernetes_config": {"kubeconfig": []}}}
		}`))
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, req)

		assert.True(t, proxyCalled)
		assert.Equal(t, http.StatusNoContent, recorder.Code)
		mockStorage.AssertNotCalled(t, "ListCluster", mock.Anything)
	})

	t.Run("rejects SSH static flow downgrade before proxy handler", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		query := url.Values{"id": []string{"eq.1"}}
		mockStorage.On("ListCluster", mock.MatchedBy(func(opt storage.ListOption) bool {
			return sameFilters(opt.Filters, queryParamsToFilters(query))
		})).Return([]v1.Cluster{
			{Spec: &v1.ClusterSpec{Type: v1.SSHClusterType, Version: "v1.1.0"}},
		}, nil)

		proxyCalled := false
		router := gin.New()
		router.PATCH("/clusters", validateClusterVersionUpdate(mockStorage), func(c *gin.Context) {
			proxyCalled = true
			c.Status(http.StatusNoContent)
		})

		req := httptest.NewRequest(http.MethodPatch, "/clusters?id=eq.1", strings.NewReader(`{
			"spec": {"version": "v1.0.1"}
		}`))
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, req)

		assert.False(t, proxyCalled)
		assert.Equal(t, http.StatusBadRequest, recorder.Code)
		assert.Contains(t, recorder.Body.String(), `"code":"10212"`)
		assert.Contains(t, recorder.Body.String(), "cluster version downgrade is not supported")
		mockStorage.AssertExpectations(t)
	})

	t.Run("allows SSH static flow version upgrade", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		query := url.Values{"id": []string{"eq.1"}}
		mockStorage.On("ListCluster", mock.MatchedBy(func(opt storage.ListOption) bool {
			return sameFilters(opt.Filters, queryParamsToFilters(query))
		})).Return([]v1.Cluster{
			{Spec: &v1.ClusterSpec{Type: v1.SSHClusterType, Version: "v1.1.0"}},
		}, nil)

		proxyCalled := false
		router := gin.New()
		router.PATCH("/clusters", validateClusterVersionUpdate(mockStorage), func(c *gin.Context) {
			proxyCalled = true
			c.Status(http.StatusNoContent)
		})

		req := httptest.NewRequest(http.MethodPatch, "/clusters?id=eq.1", strings.NewReader(`{
			"spec": {"version": "v1.2.0"}
		}`))
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, req)

		assert.True(t, proxyCalled)
		assert.Equal(t, http.StatusNoContent, recorder.Code)
		mockStorage.AssertExpectations(t)
	})

	t.Run("rejects downgrade using current spec version even when patch changes type", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		query := url.Values{"id": []string{"eq.1"}}
		mockStorage.On("ListCluster", mock.MatchedBy(func(opt storage.ListOption) bool {
			return sameFilters(opt.Filters, queryParamsToFilters(query))
		})).Return([]v1.Cluster{
			{Spec: &v1.ClusterSpec{Type: v1.SSHClusterType, Version: "v1.1.0"}},
		}, nil)

		proxyCalled := false
		router := gin.New()
		router.PATCH("/clusters", validateClusterVersionUpdate(mockStorage), func(c *gin.Context) {
			proxyCalled = true
			c.Status(http.StatusNoContent)
		})

		req := httptest.NewRequest(http.MethodPatch, "/clusters?id=eq.1", strings.NewReader(`{
			"spec": {"type": "kubernetes", "version": "v1.0.1"}
		}`))
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, req)

		assert.False(t, proxyCalled)
		assert.Equal(t, http.StatusBadRequest, recorder.Code)
		assert.Contains(t, recorder.Body.String(), "cluster version downgrade is not supported")
		mockStorage.AssertExpectations(t)
	})

	t.Run("skips storage lookup when patch does not update version", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		proxyCalled := false
		router := gin.New()
		router.PATCH("/clusters", validateClusterVersionUpdate(mockStorage), func(c *gin.Context) {
			proxyCalled = true
			c.Status(http.StatusNoContent)
		})

		req := httptest.NewRequest(http.MethodPatch, "/clusters?id=eq.1", strings.NewReader(`{
			"metadata": {"name": "cluster"}
		}`))
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, req)

		assert.True(t, proxyCalled)
		assert.Equal(t, http.StatusNoContent, recorder.Code)
		mockStorage.AssertNotCalled(t, "ListCluster", mock.Anything)
	})

	t.Run("restores request body and content length", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		body := `{"metadata":{"name":"cluster"}}`
		originalBody := &trackingReadCloser{Reader: strings.NewReader(body)}
		router := gin.New()
		router.PATCH("/clusters", validateClusterVersionUpdate(mockStorage), func(c *gin.Context) {
			restoredBody, err := io.ReadAll(c.Request.Body)
			assert.NoError(t, err)
			assert.Equal(t, body, string(restoredBody))
			assert.Equal(t, int64(len(body)), c.Request.ContentLength)
			assert.Equal(t, strconv.Itoa(len(body)), c.Request.Header.Get("Content-Length"))
			c.Status(http.StatusNoContent)
		})

		req := httptest.NewRequest(http.MethodPatch, "/clusters?id=eq.1", nil)
		req.Body = originalBody
		req.ContentLength = 0
		req.Header.Set("Content-Length", "0")
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, req)

		assert.True(t, originalBody.closed)
		assert.Equal(t, http.StatusNoContent, recorder.Code)
		mockStorage.AssertNotCalled(t, "ListCluster", mock.Anything)
	})
}

func TestRegisterClusterRoutesSoftDeleteBypassesMalformedGuardedConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mockStorage := storageMocks.NewMockStorage(t)
	mockStorage.On("Count", storage.ENDPOINT_TABLE, clusterEndpointReferenceFilters("default", "cluster")).Return(0, nil).Once()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPatch, r.Method)
		assert.Equal(t, "/clusters", r.URL.Path)

		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		assert.JSONEq(t, `{
			"metadata": {"workspace": "default", "name": "cluster", "deletion_timestamp": "2026-08-07T00:00:00Z"},
			"spec": {"config": {"kubernetes_config": {"kubeconfig": []}}}
		}`, string(body))

		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	router := gin.New()
	RegisterClusterRoutes(router.Group("/api/v1"), nil, &Dependencies{
		Storage:          mockStorage,
		StorageAccessURL: upstream.URL,
	})

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/clusters?id=eq.1", strings.NewReader(`{
		"metadata": {"workspace": "default", "name": "cluster", "deletion_timestamp": "2026-08-07T00:00:00Z"},
		"spec": {"config": {"kubernetes_config": {"kubeconfig": []}}}
	}`))
	req.Header.Set("Content-Type", "application/json")
	recorder := newCloseNotifyRecorder()

	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusNoContent, recorder.ResponseRecorder.Code)
	mockStorage.AssertExpectations(t)
	mockStorage.AssertNotCalled(t, "ListCluster", mock.Anything)
}

func testEncodedKubeconfig(server, token string) string {
	content := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: primary
  cluster:
    server: %s
contexts:
- name: primary
  context:
    cluster: primary
    user: primary
current-context: primary
users:
- name: primary
  user:
    token: %s
`, server, token)

	return base64.StdEncoding.EncodeToString([]byte(content))
}

func TestParseClusterPatchConfigurationUpdate(t *testing.T) {
	t.Run("tracks supported configuration fields", func(t *testing.T) {
		update, err := parseClusterPatchConfigurationUpdate([]byte(`{
			"spec": {
				"image_registry": "registry.example.test",
				"config": {
					"kubernetes_config": {"kubeconfig": "new-kubeconfig"},
					"ssh_config": {"auth": {"ssh_private_key": "new-private-key"}}
				}
			}
		}`))

		assert.NoError(t, err)
		assert.Equal(t, "registry.example.test", update.imageRegistry)
		assert.True(t, update.imageRegistrySet)
		assert.Equal(t, "new-kubeconfig", update.kubeconfig)
		assert.True(t, update.kubeconfigSet)
		assert.Equal(t, "new-private-key", update.sshPrivateKey)
		assert.True(t, update.sshPrivateKeySet)
		assert.True(t, update.hasChanges())
	})

	t.Run("does not treat omitted configuration fields as updates", func(t *testing.T) {
		update, err := parseClusterPatchConfigurationUpdate([]byte(`{
			"spec": {"config": {"ssh_config": {}}}
		}`))

		assert.NoError(t, err)
		assert.False(t, update.hasChanges())
	})

	t.Run("tracks cleared nested configuration", func(t *testing.T) {
		tests := []struct {
			name   string
			body   string
			assert func(*testing.T, clusterPatchConfigurationUpdate)
		}{
			{
				name: "entire config",
				body: `{"spec": {"config": null}}`,
				assert: func(t *testing.T, update clusterPatchConfigurationUpdate) {
					assert.True(t, update.configCleared)
				},
			},
			{
				name: "Kubernetes config",
				body: `{"spec": {"config": {"kubernetes_config": null}}}`,
				assert: func(t *testing.T, update clusterPatchConfigurationUpdate) {
					assert.True(t, update.kubernetesConfigCleared)
				},
			},
			{
				name: "SSH config",
				body: `{"spec": {"config": {"ssh_config": null}}}`,
				assert: func(t *testing.T, update clusterPatchConfigurationUpdate) {
					assert.True(t, update.sshConfigCleared)
				},
			},
			{
				name: "SSH auth",
				body: `{"spec": {"config": {"ssh_config": {"auth": null}}}}`,
				assert: func(t *testing.T, update clusterPatchConfigurationUpdate) {
					assert.True(t, update.sshAuthCleared)
				},
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				update, err := parseClusterPatchConfigurationUpdate([]byte(tt.body))

				assert.NoError(t, err)
				assert.True(t, update.hasChanges())
				tt.assert(t, update)
			})
		}
	})

	for _, body := range []string{
		`{`,
		`{"spec": []}`,
		`{"spec": {"image_registry": []}}`,
		`{"spec": {"config": []}}`,
		`{"spec": {"config": {"kubernetes_config": []}}}`,
		`{"spec": {"config": {"kubernetes_config": {"kubeconfig": []}}}}`,
		`{"spec": {"config": {"ssh_config": []}}}`,
		`{"spec": {"config": {"ssh_config": {"auth": []}}}}`,
		`{"spec": {"config": {"ssh_config": {"auth": {"ssh_private_key": []}}}}}`,
	} {
		_, err := parseClusterPatchConfigurationUpdate([]byte(body))
		assert.Error(t, err, body)
	}
}

func TestValidateClusterConfigurationUpdate(t *testing.T) {
	initializedKubernetesCluster := func(kubeconfig string) *v1.Cluster {
		return &v1.Cluster{
			Spec: &v1.ClusterSpec{
				Type: v1.KubernetesClusterType,
				Config: &v1.ClusterConfig{KubernetesConfig: &v1.KubernetesClusterConfig{
					Kubeconfig: kubeconfig,
				}},
			},
			Status: &v1.ClusterStatus{Initialized: true},
		}
	}

	t.Run("allows uninitialized clusters", func(t *testing.T) {
		err := validateClusterConfigurationUpdate(&v1.Cluster{
			Spec:   &v1.ClusterSpec{ImageRegistry: "current"},
			Status: &v1.ClusterStatus{Initialized: false},
		}, clusterPatchConfigurationUpdate{imageRegistry: "replacement", imageRegistrySet: true})

		assert.NoError(t, err)
	})

	t.Run("allows an unchanged image registry", func(t *testing.T) {
		err := validateClusterConfigurationUpdate(&v1.Cluster{
			Spec:   &v1.ClusterSpec{ImageRegistry: "current"},
			Status: &v1.ClusterStatus{Initialized: true},
		}, clusterPatchConfigurationUpdate{imageRegistry: "current", imageRegistrySet: true})

		assert.NoError(t, err)
	})

	t.Run("rejects unusable Kubernetes credential rotations", func(t *testing.T) {
		validCurrent := testEncodedKubeconfig("https://api.example.test:6443", "old-token")
		tests := []struct {
			name       string
			current    *v1.Cluster
			kubeconfig string
			hint       string
		}{
			{
				name:       "empty updated kubeconfig",
				current:    initializedKubernetesCluster(validCurrent),
				kubeconfig: " ",
				hint:       "kubeconfig cannot be empty",
			},
			{
				name:       "missing current Kubernetes config",
				current:    &v1.Cluster{Spec: &v1.ClusterSpec{Type: v1.KubernetesClusterType}, Status: &v1.ClusterStatus{Initialized: true}},
				kubeconfig: testEncodedKubeconfig("https://api.example.test:6443", "new-token"),
				hint:       "failed to read current kubeconfig",
			},
			{
				name:       "invalid updated kubeconfig",
				current:    initializedKubernetesCluster(validCurrent),
				kubeconfig: "not-base64",
				hint:       "failed to parse updated kubeconfig",
			},
			{
				name:       "different Kubernetes API server",
				current:    initializedKubernetesCluster(validCurrent),
				kubeconfig: testEncodedKubeconfig("https://other-api.example.test:6443", "new-token"),
				hint:       "current Kubernetes API server",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				err := validateClusterConfigurationUpdate(tt.current, clusterPatchConfigurationUpdate{
					kubeconfig:    tt.kubeconfig,
					kubeconfigSet: true,
				})

				assert.ErrorContains(t, err, tt.hint)
			})
		}
	})

	t.Run("allows same-host Kubernetes credential rotation", func(t *testing.T) {
		err := validateClusterConfigurationUpdate(
			initializedKubernetesCluster(testEncodedKubeconfig("https://api.example.test:6443", "old-token")),
			clusterPatchConfigurationUpdate{
				kubeconfig:    testEncodedKubeconfig("https://api.example.test:6443", "new-token"),
				kubeconfigSet: true,
			},
		)

		assert.NoError(t, err)
	})

	t.Run("validates SSH private-key rotations", func(t *testing.T) {
		cluster := &v1.Cluster{
			Spec:   &v1.ClusterSpec{Type: v1.SSHClusterType},
			Status: &v1.ClusterStatus{Initialized: true},
		}

		for _, tt := range []struct {
			name string
			key  string
			hint string
		}{
			{name: "empty private key", key: " ", hint: "SSH private key cannot be empty"},
			{name: "invalid base64", key: "not-base64", hint: "SSH private key must be base64 encoded"},
		} {
			t.Run(tt.name, func(t *testing.T) {
				err := validateClusterConfigurationUpdate(cluster, clusterPatchConfigurationUpdate{
					sshPrivateKey:    tt.key,
					sshPrivateKeySet: true,
				})

				assert.ErrorContains(t, err, tt.hint)
			})
		}

		err := validateClusterConfigurationUpdate(cluster, clusterPatchConfigurationUpdate{
			sshPrivateKey:    base64.StdEncoding.EncodeToString([]byte("rotated-private-key")),
			sshPrivateKeySet: true,
		})
		assert.NoError(t, err)
	})

	t.Run("rejects cleared initialized configurations", func(t *testing.T) {
		kubernetesCluster := initializedKubernetesCluster(
			testEncodedKubeconfig("https://api.example.test:6443", "old-token"),
		)
		sshCluster := &v1.Cluster{
			Spec:   &v1.ClusterSpec{Type: v1.SSHClusterType},
			Status: &v1.ClusterStatus{Initialized: true},
		}

		for _, tt := range []struct {
			name    string
			cluster *v1.Cluster
			update  clusterPatchConfigurationUpdate
			hint    string
		}{
			{
				name:    "entire config",
				cluster: kubernetesCluster,
				update:  clusterPatchConfigurationUpdate{configCleared: true},
				hint:    "config cannot be cleared",
			},
			{
				name:    "Kubernetes config",
				cluster: kubernetesCluster,
				update:  clusterPatchConfigurationUpdate{kubernetesConfigCleared: true},
				hint:    "kubernetes_config cannot be cleared",
			},
			{
				name:    "SSH config",
				cluster: sshCluster,
				update:  clusterPatchConfigurationUpdate{sshConfigCleared: true},
				hint:    "ssh_config cannot be cleared",
			},
			{
				name:    "SSH auth",
				cluster: sshCluster,
				update:  clusterPatchConfigurationUpdate{sshAuthCleared: true},
				hint:    "SSH auth cannot be cleared",
			},
		} {
			t.Run(tt.name, func(t *testing.T) {
				err := validateClusterConfigurationUpdate(tt.cluster, tt.update)

				assert.ErrorContains(t, err, tt.hint)
			})
		}
	})
}

type trackingReadCloser struct {
	*strings.Reader
	closed bool
}

func (r *trackingReadCloser) Close() error {
	r.closed = true

	return nil
}

func TestValidateClusterVersionNotDowngrade(t *testing.T) {
	tests := []struct {
		name            string
		current         *v1.Cluster
		desiredVersion  string
		wantErrContains string
	}{
		{
			name:           "allows legacy to static upgrade",
			current:        &v1.Cluster{Spec: &v1.ClusterSpec{Type: v1.SSHClusterType, Version: "v1.0.1"}},
			desiredVersion: "v1.1.0",
		},
		{
			name:           "allows static flow upgrade",
			current:        &v1.Cluster{Spec: &v1.ClusterSpec{Type: v1.SSHClusterType, Version: "v1.1.0"}},
			desiredVersion: "v1.2.0",
		},
		{
			name:           "allows static flow same version",
			current:        &v1.Cluster{Spec: &v1.ClusterSpec{Type: v1.SSHClusterType, Version: "v1.1.0"}},
			desiredVersion: "v1.1.0",
		},
		{
			name:            "rejects static flow downgrade within static flow",
			current:         &v1.Cluster{Spec: &v1.ClusterSpec{Type: v1.SSHClusterType, Version: "v1.1.0"}},
			desiredVersion:  "v1.0.2",
			wantErrContains: "cluster version downgrade is not supported",
		},
		{
			name:            "rejects static flow downgrade to legacy flow",
			current:         &v1.Cluster{Spec: &v1.ClusterSpec{Type: v1.SSHClusterType, Version: "v1.1.0"}},
			desiredVersion:  "v1.0.1",
			wantErrContains: "cluster version downgrade is not supported",
		},
		{
			name:            "rejects static flow downgrade below legacy gate",
			current:         &v1.Cluster{Spec: &v1.ClusterSpec{Type: v1.SSHClusterType, Version: "v1.1.0"}},
			desiredVersion:  "v1.0.0",
			wantErrContains: "cluster version downgrade is not supported",
		},
		{
			name:           "allows Kubernetes version upgrade",
			current:        &v1.Cluster{Spec: &v1.ClusterSpec{Type: v1.KubernetesClusterType, Version: "v1.1.0"}},
			desiredVersion: "v1.2.0",
		},
		{
			name:            "rejects Kubernetes version downgrade",
			current:         &v1.Cluster{Spec: &v1.ClusterSpec{Type: v1.KubernetesClusterType, Version: "v1.1.0"}},
			desiredVersion:  "v1.0.1",
			wantErrContains: "cluster version downgrade is not supported",
		},
		{
			name: "allows update equal to spec version when status version is newer",
			current: &v1.Cluster{
				Spec:   &v1.ClusterSpec{Type: v1.KubernetesClusterType, Version: "v1.1.0"},
				Status: &v1.ClusterStatus{Version: "v1.2.0"},
			},
			desiredVersion: "v1.1.0",
		},
		{
			name: "allows update when spec version is empty even if status version is newer",
			current: &v1.Cluster{
				Spec:   &v1.ClusterSpec{Type: v1.KubernetesClusterType},
				Status: &v1.ClusterStatus{Version: "v1.2.0"},
			},
			desiredVersion: "v1.1.0",
		},
		{
			name: "allows SSH update when spec version is empty even if status version is static flow",
			current: &v1.Cluster{
				Spec:   &v1.ClusterSpec{Type: v1.SSHClusterType},
				Status: &v1.ClusterStatus{Version: "v1.2.0"},
			},
			desiredVersion: "v1.0.1",
		},
		{
			name:           "allows update when current spec version is invalid",
			current:        &v1.Cluster{Spec: &v1.ClusterSpec{Type: v1.KubernetesClusterType, Version: "custom"}},
			desiredVersion: "v1.1.0",
		},
		{
			name:            "rejects invalid desired version when current spec version is invalid",
			current:         &v1.Cluster{Spec: &v1.ClusterSpec{Type: v1.KubernetesClusterType, Version: "custom"}},
			desiredVersion:  "also-custom",
			wantErrContains: "invalid desired cluster version",
		},
		{
			name:            "rejects invalid desired version when current SSH cluster is legacy flow",
			current:         &v1.Cluster{Spec: &v1.ClusterSpec{Type: v1.SSHClusterType, Version: "v1.0.1"}},
			desiredVersion:  "custom",
			wantErrContains: "invalid desired cluster version",
		},
		{
			name:            "rejects invalid desired version when current SSH cluster is static flow",
			current:         &v1.Cluster{Spec: &v1.ClusterSpec{Type: v1.SSHClusterType, Version: "v1.1.0"}},
			desiredVersion:  "custom",
			wantErrContains: "invalid desired cluster version",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateClusterVersionNotDowngrade(tt.current, tt.desiredVersion)
			if tt.wantErrContains == "" {
				assert.NoError(t, err)
				return
			}

			if !assert.Error(t, err) {
				return
			}
			assert.Contains(t, err.Error(), tt.wantErrContains)
		})
	}
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

	t.Run("true when accelerator virtualization patch is null", func(t *testing.T) {
		requested, err := clusterAcceleratorVirtualizationDisableRequested([]byte(`{
			"spec": {
				"accelerator_virtualization": null
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
