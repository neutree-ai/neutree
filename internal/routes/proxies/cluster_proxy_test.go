package proxies

import (
	"errors"
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
}

func TestValidateClusterVersionUpdateMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("rejects SSH static flow downgrade to legacy flow before proxy handler", func(t *testing.T) {
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
		assert.Contains(t, recorder.Body.String(), "static flow to legacy flow is not supported")
		mockStorage.AssertExpectations(t)
	})

	t.Run("allows SSH static flow version update", func(t *testing.T) {
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
			"spec": {"version": "v1.0.2"}
		}`))
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, req)

		assert.True(t, proxyCalled)
		assert.Equal(t, http.StatusNoContent, recorder.Code)
		mockStorage.AssertExpectations(t)
	})

	t.Run("rejects static flow downgrade using current cluster type even when patch changes type", func(t *testing.T) {
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
		assert.Contains(t, recorder.Body.String(), "static flow to legacy flow is not supported")
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

type trackingReadCloser struct {
	*strings.Reader
	closed bool
}

func (r *trackingReadCloser) Close() error {
	r.closed = true

	return nil
}

func TestValidateStaticNodeClusterFlowVersionUpdate(t *testing.T) {
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
			name:           "allows static flow version update",
			current:        &v1.Cluster{Spec: &v1.ClusterSpec{Type: v1.SSHClusterType, Version: "v1.1.0"}},
			desiredVersion: "v1.0.2",
		},
		{
			name:            "rejects static flow downgrade to legacy flow",
			current:         &v1.Cluster{Spec: &v1.ClusterSpec{Type: v1.SSHClusterType, Version: "v1.1.0"}},
			desiredVersion:  "v1.0.1",
			wantErrContains: "static flow to legacy flow is not supported",
		},
		{
			name:            "rejects static flow downgrade below legacy gate",
			current:         &v1.Cluster{Spec: &v1.ClusterSpec{Type: v1.SSHClusterType, Version: "v1.1.0"}},
			desiredVersion:  "v1.0.0",
			wantErrContains: "static flow to legacy flow is not supported",
		},
		{
			name:           "allows Kubernetes version downgrade",
			current:        &v1.Cluster{Spec: &v1.ClusterSpec{Type: v1.KubernetesClusterType, Version: "v1.1.0"}},
			desiredVersion: "v1.0.1",
		},
		{
			name:           "allows invalid desired version when current SSH cluster is legacy flow",
			current:        &v1.Cluster{Spec: &v1.ClusterSpec{Type: v1.SSHClusterType, Version: "v1.0.1"}},
			desiredVersion: "custom",
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
			err := validateStaticNodeClusterFlowVersionUpdate(tt.current, tt.desiredVersion)
			if tt.wantErrContains == "" {
				assert.NoError(t, err)
				return
			}

			assert.Error(t, err)
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
