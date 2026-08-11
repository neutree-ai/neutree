package proxies

import (
	"encoding/base64"
	"encoding/json"
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

func TestValidateClusterSoftDelete(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name            string
		path            string
		body            string
		endpointCount   int
		countError      error
		expectCount     bool
		expectProxy     bool
		expectPoweredBy bool
		expectedStatus  int
		expectedCode    string
		expectedHint    string
	}{
		{
			name: "allows incomplete identity",
			path: "/clusters",
			body: `{
				"metadata": {"deletion_timestamp": "2026-08-10T00:00:00Z"}
			}`,
			expectProxy:    true,
			expectedStatus: http.StatusNoContent,
		},
		{
			name: "bypasses malformed guarded configuration",
			path: "/clusters?id=eq.1",
			body: `{
				"metadata": {
					"workspace": "default",
					"name": "cluster",
					"deletion_timestamp": "2026-08-10T00:00:00Z"
				},
				"spec": {"config": {"kubernetes_config": {"kubeconfig": []}}}
			}`,
			endpointCount:  0,
			expectCount:    true,
			expectProxy:    true,
			expectedStatus: http.StatusNoContent,
		},
		{
			name: "rejects referenced cluster",
			path: "/clusters",
			body: `{
				"metadata": {
					"workspace": "default",
					"name": "cluster",
					"deletion_timestamp": "2026-08-10T00:00:00Z"
				}
			}`,
			endpointCount:   1,
			expectCount:     true,
			expectedStatus:  http.StatusBadRequest,
			expectedCode:    "10126",
			expectedHint:    "1 endpoint(s) still reference this cluster",
			expectPoweredBy: true,
		},
		{
			name: "returns internal error when dependency lookup fails",
			path: "/clusters",
			body: `{
				"metadata": {
					"workspace": "default",
					"name": "cluster",
					"deletion_timestamp": "2026-08-10T00:00:00Z"
				}
			}`,
			countError:     errors.New("count endpoints failed"),
			expectCount:    true,
			expectedStatus: http.StatusInternalServerError,
			expectedCode:   "500",
			expectedHint:   "Failed to validate deletion",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := storageMocks.NewMockStorage(t)
			if tt.expectCount {
				mockStorage.On(
					"Count",
					storage.ENDPOINT_TABLE,
					clusterEndpointReferenceFilters("default", "cluster"),
				).Return(tt.endpointCount, tt.countError).Once()
			}

			proxyCalled := false
			router := gin.New()
			router.PATCH("/clusters", validateClusterRequest(mockStorage), func(c *gin.Context) {
				proxyCalled = true

				forwardedBody, err := io.ReadAll(c.Request.Body)
				assert.NoError(t, err)
				assert.Equal(t, tt.body, string(forwardedBody))
				assert.Equal(t, int64(len(tt.body)), c.Request.ContentLength)
				assert.Equal(t, strconv.Itoa(len(tt.body)), c.Request.Header.Get("Content-Length"))
				c.Status(http.StatusNoContent)
			})

			originalBody := &trackingReadCloser{Reader: strings.NewReader(tt.body)}
			req := httptest.NewRequest(http.MethodPatch, tt.path, nil)
			req.Body = originalBody
			req.ContentLength = 0
			req.Header.Set("Content-Length", "0")
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, req)

			assert.Equal(t, tt.expectProxy, proxyCalled)
			assert.True(t, originalBody.closed)
			assert.Equal(t, tt.expectedStatus, recorder.Code)
			if tt.expectPoweredBy {
				assert.Equal(t, "Neutree", recorder.Header().Get("X-Powered-By"))
			}
			if tt.expectedCode != "" {
				assert.Contains(t, recorder.Body.String(), `"code":"`+tt.expectedCode+`"`)
			}
			if tt.expectedHint != "" {
				assert.Contains(t, recorder.Body.String(), tt.expectedHint)
			}
			mockStorage.AssertExpectations(t)
			if !tt.expectCount {
				mockStorage.AssertNotCalled(t, "Count", mock.Anything)
			}
			mockStorage.AssertNotCalled(t, "ListCluster", mock.Anything)
			mockStorage.AssertNotCalled(t, "ListEndpoint", mock.Anything)
		})
	}

	t.Run("forwards malformed guarded configuration through registered route", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		mockStorage.On(
			"Count",
			storage.ENDPOINT_TABLE,
			clusterEndpointReferenceFilters("default", "cluster"),
		).Return(0, nil).Once()

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
	})
}

func TestValidateClusterRequestMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("passes through non-mutating requests", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		proxyCalled := false
		router := gin.New()
		router.GET("/clusters", validateClusterRequest(mockStorage), func(c *gin.Context) {
			proxyCalled = true
			c.Status(http.StatusNoContent)
		})

		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/clusters", nil))

		assert.True(t, proxyCalled)
		assert.Equal(t, http.StatusNoContent, recorder.Code)
		mockStorage.AssertNotCalled(t, "Count", mock.Anything)
		mockStorage.AssertNotCalled(t, "ListCluster", mock.Anything)
		mockStorage.AssertNotCalled(t, "ListEndpoint", mock.Anything)
	})

	t.Run("allows an empty create body", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		proxyCalled := false
		router := gin.New()
		router.POST("/clusters", validateClusterRequest(mockStorage), func(c *gin.Context) {
			proxyCalled = true
			c.Status(http.StatusNoContent)
		})

		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/clusters", nil))

		assert.True(t, proxyCalled)
		assert.Equal(t, http.StatusNoContent, recorder.Code)
		mockStorage.AssertNotCalled(t, "Count", mock.Anything)
		mockStorage.AssertNotCalled(t, "ListCluster", mock.Anything)
		mockStorage.AssertNotCalled(t, "ListEndpoint", mock.Anything)
	})

	t.Run("rejects JSON that cannot be decoded as a cluster object", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		proxyCalled := false
		router := gin.New()
		router.PATCH("/clusters", validateClusterRequest(mockStorage), func(c *gin.Context) {
			proxyCalled = true
			c.Status(http.StatusNoContent)
		})

		req := httptest.NewRequest(http.MethodPatch, "/clusters", strings.NewReader(`[]`))
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, req)

		assert.False(t, proxyCalled)
		assert.Equal(t, http.StatusBadRequest, recorder.Code)
		assert.Contains(t, recorder.Body.String(), `"code":"10209"`)
		assert.Contains(t, recorder.Body.String(), "invalid cluster payload")
		mockStorage.AssertNotCalled(t, "Count", mock.Anything)
		mockStorage.AssertNotCalled(t, "ListCluster", mock.Anything)
		mockStorage.AssertNotCalled(t, "ListEndpoint", mock.Anything)
	})

	t.Run("dispatches POST to accelerator virtualization validation", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		proxyCalled := false
		router := gin.New()
		router.POST("/clusters", validateClusterRequest(mockStorage), func(c *gin.Context) {
			proxyCalled = true
			c.Status(http.StatusNoContent)
		})

		req := httptest.NewRequest(http.MethodPost, "/clusters", strings.NewReader(`{
			"spec": {
				"type": "ssh",
				"accelerator_virtualization": {"enabled": true}
			}
		}`))
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, req)

		assert.False(t, proxyCalled)
		assert.Equal(t, http.StatusBadRequest, recorder.Code)
		assert.Contains(t, recorder.Body.String(), `"code":"10208"`)
		mockStorage.AssertNotCalled(t, "Count", mock.Anything)
		mockStorage.AssertNotCalled(t, "ListCluster", mock.Anything)
		mockStorage.AssertNotCalled(t, "ListEndpoint", mock.Anything)
	})

	t.Run("dispatches normal PATCH to configuration validation", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		query := url.Values{"id": []string{"eq.1"}}
		mockStorage.On("ListCluster", mock.MatchedBy(func(opt storage.ListOption) bool {
			return sameFilters(opt.Filters, queryParamsToFilters(query))
		})).Return([]v1.Cluster{{
			Spec:   &v1.ClusterSpec{ImageRegistry: "current-registry"},
			Status: &v1.ClusterStatus{Initialized: true},
		}}, nil).Once()

		proxyCalled := false
		router := gin.New()
		router.PATCH("/clusters", validateClusterRequest(mockStorage), func(c *gin.Context) {
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
		assert.Contains(t, recorder.Body.String(), "image registry cannot be changed")
		mockStorage.AssertExpectations(t)
		mockStorage.AssertNotCalled(t, "Count", mock.Anything)
		mockStorage.AssertNotCalled(t, "ListEndpoint", mock.Anything)
	})

	t.Run("preserves accelerator virtualization when a sparse spec omits it", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		query := url.Values{"id": []string{"eq.1"}}
		mockStorage.On("ListCluster", mock.MatchedBy(func(opt storage.ListOption) bool {
			return sameFilters(opt.Filters, queryParamsToFilters(query))
		})).Return([]v1.Cluster{{
			Metadata: &v1.Metadata{Workspace: "default", Name: "gpu-cluster"},
			Spec: &v1.ClusterSpec{
				Type:    v1.KubernetesClusterType,
				Version: "v1.0.0",
				AcceleratorVirtualization: &v1.AcceleratorVirtualizationSpec{
					Enabled: true,
				},
			},
		}}, nil).Once()
		proxyCalled := false
		router := gin.New()
		router.PATCH("/clusters", validateClusterRequest(mockStorage), func(c *gin.Context) {
			proxyCalled = true
			c.Status(http.StatusNoContent)
		})

		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPatch, "/clusters?id=eq.1", strings.NewReader(`{
				"spec": {"version": "v1.1.0"}
			}`)))

		assert.True(t, proxyCalled)
		assert.Equal(t, http.StatusNoContent, recorder.Code)
		mockStorage.AssertExpectations(t)
		mockStorage.AssertNotCalled(t, "ListEndpoint", mock.Anything)
	})

	t.Run("resolves the current cluster once for all normal PATCH validators", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		query := url.Values{"id": []string{"eq.1"}}
		mockStorage.On("ListCluster", mock.MatchedBy(func(opt storage.ListOption) bool {
			return sameFilters(opt.Filters, queryParamsToFilters(query))
		})).Return([]v1.Cluster{{
			Spec: &v1.ClusterSpec{
				ImageRegistry: "current-registry",
				Version:       "v1.0.0",
			},
			Status: &v1.ClusterStatus{Initialized: true},
		}}, nil).Once()

		proxyCalled := false
		router := gin.New()
		router.PATCH("/clusters", validateClusterRequest(mockStorage), func(c *gin.Context) {
			proxyCalled = true
			body, err := io.ReadAll(c.Request.Body)
			assert.NoError(t, err)
			assert.JSONEq(t, `{
				"spec": {"version": "v1.1.0", "image_registry": "current-registry"}
			}`, string(body))
			c.Status(http.StatusNoContent)
		})

		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPatch, "/clusters?id=eq.1", strings.NewReader(`{
			"spec": {"version": "v1.1.0", "image_registry": "current-registry"}
		}`)))

		assert.True(t, proxyCalled)
		assert.Equal(t, http.StatusNoContent, recorder.Code)
		mockStorage.AssertExpectations(t)
		mockStorage.AssertNotCalled(t, "Count", mock.Anything)
		mockStorage.AssertNotCalled(t, "ListEndpoint", mock.Anything)
	})

	t.Run("keeps a masked kubeconfig null in the forwarded PATCH body", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		query := url.Values{"id": []string{"eq.1"}}
		mockStorage.On("ListCluster", mock.MatchedBy(func(opt storage.ListOption) bool {
			return sameFilters(opt.Filters, queryParamsToFilters(query))
		})).Return([]v1.Cluster{{
			Spec: &v1.ClusterSpec{
				Type: v1.KubernetesClusterType,
				Config: &v1.ClusterConfig{KubernetesConfig: &v1.KubernetesClusterConfig{
					Kubeconfig: "current-kubeconfig",
				}},
			},
			Status: &v1.ClusterStatus{Initialized: true},
		}}, nil).Once()

		body := `{"spec":{"config":{"kubernetes_config":{"kubeconfig":null}}}}`
		proxyCalled := false
		router := gin.New()
		router.PATCH("/clusters", validateClusterRequest(mockStorage), func(c *gin.Context) {
			proxyCalled = true
			forwardedBody, err := io.ReadAll(c.Request.Body)
			assert.NoError(t, err)
			assert.Equal(t, body, string(forwardedBody))
			c.Status(http.StatusNoContent)
		})

		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPatch, "/clusters?id=eq.1", strings.NewReader(body)))

		assert.True(t, proxyCalled)
		assert.Equal(t, http.StatusNoContent, recorder.Code)
		mockStorage.AssertExpectations(t)
	})

	t.Run("preserves the version resolution error when PATCH has no identity", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		proxyCalled := false
		router := gin.New()
		router.PATCH("/clusters", validateClusterRequest(mockStorage), func(c *gin.Context) {
			proxyCalled = true
			c.Status(http.StatusNoContent)
		})

		req := httptest.NewRequest(http.MethodPatch, "/clusters", strings.NewReader(`{
			"spec": {"version": "v1.2.0"}
		}`))
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, req)

		assert.False(t, proxyCalled)
		assert.Equal(t, http.StatusBadRequest, recorder.Code)
		assert.Contains(t, recorder.Body.String(), `"code":"10212"`)
		assert.Contains(t, recorder.Body.String(), "failed to validate cluster version update")
		assert.Contains(t, recorder.Body.String(), "cluster identity is required when updating spec.version")
		mockStorage.AssertNotCalled(t, "ListCluster", mock.Anything)
	})

	t.Run("preserves the version resolution error when the current cluster lookup fails", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		query := url.Values{"id": []string{"eq.1"}}
		mockStorage.On("ListCluster", mock.MatchedBy(func(opt storage.ListOption) bool {
			return sameFilters(opt.Filters, queryParamsToFilters(query))
		})).Return(nil, errors.New("read cluster failed")).Once()

		proxyCalled := false
		router := gin.New()
		router.PATCH("/clusters", validateClusterRequest(mockStorage), func(c *gin.Context) {
			proxyCalled = true
			c.Status(http.StatusNoContent)
		})

		req := httptest.NewRequest(http.MethodPatch, "/clusters?id=eq.1", strings.NewReader(`{
			"spec": {"version": "v1.2.0"}
		}`))
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, req)

		assert.False(t, proxyCalled)
		assert.Equal(t, http.StatusBadRequest, recorder.Code)
		assert.Contains(t, recorder.Body.String(), `"code":"10212"`)
		assert.Contains(t, recorder.Body.String(), "failed to validate cluster version update")
		assert.Contains(t, recorder.Body.String(), "read cluster failed")
		mockStorage.AssertExpectations(t)
	})

	t.Run("preserves the configuration resolution error when PATCH has no identity", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		proxyCalled := false
		router := gin.New()
		router.PATCH("/clusters", validateClusterRequest(mockStorage), func(c *gin.Context) {
			proxyCalled = true
			c.Status(http.StatusNoContent)
		})

		req := httptest.NewRequest(http.MethodPatch, "/clusters", strings.NewReader(`{
			"spec": {"image_registry": "replacement-registry"}
		}`))
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, req)

		assert.False(t, proxyCalled)
		assert.Equal(t, http.StatusBadRequest, recorder.Code)
		assert.Contains(t, recorder.Body.String(), `"code":"10209"`)
		assert.Contains(t, recorder.Body.String(), "failed to validate cluster configuration update")
		assert.Contains(t, recorder.Body.String(), "cluster identity is required when updating cluster configuration")
		mockStorage.AssertNotCalled(t, "ListCluster", mock.Anything)
	})

	t.Run("uses the generic preparation error for an unrelated PATCH without identity", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		proxyCalled := false
		router := gin.New()
		router.PATCH("/clusters", validateClusterRequest(mockStorage), func(c *gin.Context) {
			proxyCalled = true
			c.Status(http.StatusNoContent)
		})

		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPatch, "/clusters", strings.NewReader(`{
			"metadata": {"name": "cluster"}
		}`)))

		assert.False(t, proxyCalled)
		assert.Equal(t, http.StatusBadRequest, recorder.Code)
		assert.Contains(t, recorder.Body.String(), `"code":"10209"`)
		assert.Contains(t, recorder.Body.String(), "failed to prepare cluster patch validation")
		assert.Contains(t, recorder.Body.String(), "cluster identity is required when patching a cluster")
		mockStorage.AssertNotCalled(t, "ListCluster", mock.Anything)
	})

	t.Run("preserves the accelerator virtualization identity error when PATCH has no identity", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		proxyCalled := false
		router := gin.New()
		router.PATCH("/clusters", validateClusterRequest(mockStorage), func(c *gin.Context) {
			proxyCalled = true
			c.Status(http.StatusNoContent)
		})

		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPatch, "/clusters", strings.NewReader(`{
			"spec": {"accelerator_virtualization": {}}
		}`)))

		assert.False(t, proxyCalled)
		assert.Equal(t, http.StatusBadRequest, recorder.Code)
		assert.Contains(t, recorder.Body.String(), `"code":"10209"`)
		assert.Contains(t, recorder.Body.String(), "failed to validate cluster accelerator virtualization")
		assert.Contains(t, recorder.Body.String(), "cluster identity is required when disabling accelerator virtualization")
		mockStorage.AssertNotCalled(t, "ListCluster", mock.Anything)
	})

	t.Run("uses the generic identity error for an accelerator virtualization enable PATCH", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		proxyCalled := false
		router := gin.New()
		router.PATCH("/clusters", validateClusterRequest(mockStorage), func(c *gin.Context) {
			proxyCalled = true
			c.Status(http.StatusNoContent)
		})

		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPatch, "/clusters", strings.NewReader(`{
			"spec": {"accelerator_virtualization": {"enabled": true}}
		}`)))

		assert.False(t, proxyCalled)
		assert.Equal(t, http.StatusBadRequest, recorder.Code)
		assert.Contains(t, recorder.Body.String(), `"code":"10209"`)
		assert.Contains(t, recorder.Body.String(), "failed to prepare cluster patch validation")
		assert.Contains(t, recorder.Body.String(), "cluster identity is required when patching a cluster")
		mockStorage.AssertNotCalled(t, "ListCluster", mock.Anything)
	})

	t.Run("preserves the accelerator virtualization resolution error when the current cluster lookup fails", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		query := url.Values{"id": []string{"eq.1"}}
		mockStorage.On("ListCluster", mock.MatchedBy(func(opt storage.ListOption) bool {
			return sameFilters(opt.Filters, queryParamsToFilters(query))
		})).Return(nil, errors.New("read cluster failed")).Once()

		proxyCalled := false
		router := gin.New()
		router.PATCH("/clusters", validateClusterRequest(mockStorage), func(c *gin.Context) {
			proxyCalled = true
			c.Status(http.StatusNoContent)
		})

		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPatch, "/clusters?id=eq.1", strings.NewReader(`{
			"spec": {"accelerator_virtualization": {}}
		}`)))

		assert.False(t, proxyCalled)
		assert.Equal(t, http.StatusBadRequest, recorder.Code)
		assert.Contains(t, recorder.Body.String(), `"code":"10209"`)
		assert.Contains(t, recorder.Body.String(), "failed to validate cluster accelerator virtualization")
		assert.Contains(t, recorder.Body.String(), "read cluster failed")
		mockStorage.AssertExpectations(t)
	})

	t.Run("reports a fixed configuration resolution error when the target is ambiguous", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		query := url.Values{"id": []string{"eq.1"}}
		mockStorage.On("ListCluster", mock.MatchedBy(func(opt storage.ListOption) bool {
			return sameFilters(opt.Filters, queryParamsToFilters(query))
		})).Return([]v1.Cluster{}, nil).Once()

		proxyCalled := false
		router := gin.New()
		router.PATCH("/clusters", validateClusterRequest(mockStorage), func(c *gin.Context) {
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
		assert.Contains(t, recorder.Body.String(), "expected exactly one cluster")
		mockStorage.AssertExpectations(t)
	})

	t.Run("preserves accelerator virtualization when its patch object omits enabled", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		query := url.Values{"id": []string{"eq.1"}}
		mockStorage.On("ListCluster", mock.MatchedBy(func(opt storage.ListOption) bool {
			return sameFilters(opt.Filters, queryParamsToFilters(query))
		})).Return([]v1.Cluster{{
			Metadata: &v1.Metadata{Workspace: "default", Name: "cluster"},
			Spec: &v1.ClusterSpec{
				Type:                      v1.KubernetesClusterType,
				Version:                   "v1.1.0",
				AcceleratorVirtualization: &v1.AcceleratorVirtualizationSpec{Enabled: true},
			},
		}}, nil).Once()

		proxyCalled := false
		router := gin.New()
		router.PATCH("/clusters", validateClusterRequest(mockStorage), func(c *gin.Context) {
			proxyCalled = true
			c.Status(http.StatusNoContent)
		})

		req := httptest.NewRequest(http.MethodPatch, "/clusters?id=eq.1", strings.NewReader(`{
			"spec": {"accelerator_virtualization": {}}
		}`))
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, req)

		assert.True(t, proxyCalled)
		assert.Equal(t, http.StatusNoContent, recorder.Code)
		mockStorage.AssertExpectations(t)
		mockStorage.AssertNotCalled(t, "ListEndpoint", mock.Anything)
	})

	t.Run("rejects malformed PATCH before any validator lookup", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		proxyCalled := false
		router := gin.New()
		router.PATCH("/clusters", validateClusterRequest(mockStorage), func(c *gin.Context) {
			proxyCalled = true
			c.Status(http.StatusNoContent)
		})

		req := httptest.NewRequest(http.MethodPatch, "/clusters?id=eq.1", strings.NewReader(`{
			"spec": {"config": {"kubernetes_config": {"kubeconfig": []}}}
		}`))
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, req)

		assert.False(t, proxyCalled)
		assert.Equal(t, http.StatusBadRequest, recorder.Code)
		assert.Contains(t, recorder.Body.String(), `"code":"10209"`)
		assert.Contains(t, recorder.Body.String(), "invalid cluster payload")
		mockStorage.AssertNotCalled(t, "Count", mock.Anything)
		mockStorage.AssertNotCalled(t, "ListCluster", mock.Anything)
		mockStorage.AssertNotCalled(t, "ListEndpoint", mock.Anything)
	})
}

func TestValidateClusterAcceleratorVirtualization(t *testing.T) {
	t.Run("body", testValidateClusterAcceleratorVirtualizationBody)
	t.Run("disable for current", testValidateClusterAcceleratorVirtualizationDisableForCurrent)
	t.Run("disable", testValidateClusterAcceleratorVirtualizationDisable)
	t.Run("middleware", testValidateClusterAcceleratorVirtualizationMiddleware)
}

func testValidateClusterAcceleratorVirtualizationDisableForCurrent(t *testing.T) {
	mockStorage := storageMocks.NewMockStorage(t)
	current := &v1.Cluster{Metadata: &v1.Metadata{Workspace: "default", Name: "cluster"}}
	patch := v1.Cluster{Metadata: &v1.Metadata{Workspace: "default", Name: "other-cluster"}}

	validationErr := validateClusterAcceleratorVirtualizationDisableForCurrent(mockStorage, current, patch)

	assert.NotNil(t, validationErr)
	assert.Equal(t, "10209", validationErr.Code)
	assert.Equal(t, "failed to validate cluster accelerator virtualization", validationErr.Message)
	assert.Equal(t, "cluster metadata in patch body does not match patch target", validationErr.Hint)
	mockStorage.AssertNotCalled(t, "ListEndpoint", mock.Anything)
}

func testValidateClusterAcceleratorVirtualizationBody(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantCode    string
		wantMessage string
	}{
		{
			name: "allows Kubernetes cluster to enable accelerator virtualization",
			body: `{"spec":{"type":"kubernetes","version":"v1.1.0","accelerator_virtualization":{"enabled":true,"config_patch":{"devicePlugin":{"nvidiaDriverRoot":"/run/nvidia/driver"}}}}}`,
		},
		{
			name: "allows Kubernetes nightly cluster with minimum base version to enable accelerator virtualization",
			body: `{"spec":{"type":"kubernetes","version":"v1.1.0-nightly-20260603","accelerator_virtualization":{"enabled":true}}}`,
		},
		{
			name:        "rejects Kubernetes cluster below minimum version enabling accelerator virtualization",
			body:        `{"spec":{"type":"kubernetes","version":"v1.0.9","accelerator_virtualization":{"enabled":true}}}`,
			wantCode:    "10208",
			wantMessage: "requires cluster version >= v1.1.0",
		},
		{
			name:        "rejects Kubernetes cluster missing version enabling accelerator virtualization",
			body:        `{"spec":{"type":"kubernetes","accelerator_virtualization":{"enabled":true}}}`,
			wantCode:    "10208",
			wantMessage: "requires cluster version >= v1.1.0",
		},
		{
			name:        "rejects invalid cluster version enabling accelerator virtualization",
			body:        `{"spec":{"type":"kubernetes","version":"nightly","accelerator_virtualization":{"enabled":true}}}`,
			wantCode:    "10209",
			wantMessage: "invalid cluster version",
		},
		{
			name:     "rejects SSH cluster enabling accelerator virtualization",
			body:     `{"spec":{"type":"ssh","accelerator_virtualization":{"enabled":true}}}`,
			wantCode: "10208",
		},
		{
			name:        "rejects non-bool enabled",
			body:        `{"spec":{"type":"kubernetes","version":"v1.1.0","accelerator_virtualization":{"enabled":"true"}}}`,
			wantCode:    "10209",
			wantMessage: "invalid cluster payload",
		},
		{
			name:        "rejects non-object config_patch",
			body:        `{"spec":{"type":"kubernetes","version":"v1.1.0","accelerator_virtualization":{"enabled":true,"config_patch":["invalid"]}}}`,
			wantCode:    "10209",
			wantMessage: "invalid cluster payload",
		},
		{
			name: "skips accelerator virtualization validation for soft delete patch",
			body: `{"metadata":{"name":"cluster","workspace":"default","deletion_timestamp":"2026-06-10T00:00:00Z"},"spec":{"type":"ssh","accelerator_virtualization":{"enabled":true}}}`,
		},
		{
			name:        "rejects unsupported config patch key",
			body:        `{"spec":{"type":"kubernetes","version":"v1.1.0","accelerator_virtualization":{"enabled":true,"config_patch":{"dra":{"enabled":true}}}}}`,
			wantCode:    "10210",
			wantMessage: "unsupported",
		},
		{
			name:        "rejects MIG virtualization config patch",
			body:        `{"spec":{"type":"kubernetes","version":"v1.1.0","accelerator_virtualization":{"enabled":true,"config_patch":{"devicePlugin":{"migStrategy":"mixed"}}}}}`,
			wantCode:    "10210",
			wantMessage: "MIG",
		},
		{
			name:        "rejects partial patch missing cluster type and version",
			body:        `{"metadata":{"name":"cluster","workspace":"default"},"spec":{"accelerator_virtualization":{"enabled":true}}}`,
			wantCode:    "10208",
			wantMessage: "only supported for Kubernetes",
		},
		{
			name:        "rejects partial patch missing cluster version",
			body:        `{"metadata":{"name":"cluster","workspace":"default"},"spec":{"type":"kubernetes","accelerator_virtualization":{"enabled":true}}}`,
			wantCode:    "10208",
			wantMessage: "requires cluster version >= v1.1.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateClusterAcceleratorVirtualizationBody([]byte(tt.body))
			if tt.wantCode == "" {
				assert.Nil(t, err)

				return
			}

			if !assert.NotNil(t, err) {
				return
			}
			assert.Equal(t, tt.wantCode, err.Code)
			if tt.wantMessage != "" {
				assert.Contains(t, err.Message, tt.wantMessage)
			}
		})
	}
}

func testValidateClusterAcceleratorVirtualizationDisable(t *testing.T) {
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

	cluster := func(name string, acceleratorVirtualization *v1.AcceleratorVirtualizationSpec) v1.Cluster {
		return v1.Cluster{
			Metadata: &v1.Metadata{Workspace: "default", Name: name},
			Spec:     &v1.ClusterSpec{AcceleratorVirtualization: acceleratorVirtualization},
		}
	}

	tests := []struct {
		name            string
		patch           v1.Cluster
		query           url.Values
		resolved        *v1.Cluster
		endpoints       []v1.Endpoint
		endpointErr     error
		lookupEndpoints bool
		wantCode        string
		wantMessage     string
		wantHint        string
	}{
		{
			name:            "rejects disabling when vGPU endpoint references cluster",
			patch:           cluster("gpu-cluster", &v1.AcceleratorVirtualizationSpec{Enabled: false}),
			endpoints:       []v1.Endpoint{vGPUEndpoint},
			lookupEndpoints: true,
			wantCode:        "10211",
			wantMessage:     "cannot disable accelerator virtualization",
			wantHint:        "1 vGPU endpoint(s) still reference this cluster",
		},
		{
			name:            "allows disabling when only non-vGPU endpoint references cluster",
			patch:           cluster("gpu-cluster", &v1.AcceleratorVirtualizationSpec{Enabled: false}),
			endpoints:       []v1.Endpoint{nonVGPUEndpoint},
			lookupEndpoints: true,
		},
		{
			name: "resolves cluster identity from patch query filters",
			patch: v1.Cluster{Spec: &v1.ClusterSpec{
				AcceleratorVirtualization: &v1.AcceleratorVirtualizationSpec{Enabled: false},
			}},
			query: url.Values{
				"metadata->>workspace": {"eq.default"},
				"metadata->>name":      {"eq.gpu-cluster"},
			},
			resolved:        &v1.Cluster{Metadata: &v1.Metadata{Workspace: "default", Name: "gpu-cluster"}},
			endpoints:       []v1.Endpoint{vGPUEndpoint},
			lookupEndpoints: true,
			wantCode:        "10211",
			wantHint:        "1 vGPU endpoint(s) still reference this cluster",
		},
		{
			name:  "rejects mismatched patch body identity and query target",
			patch: cluster("body-cluster", &v1.AcceleratorVirtualizationSpec{Enabled: false}),
			query: url.Values{
				"id": {"eq.1"},
			},
			resolved: &v1.Cluster{Metadata: &v1.Metadata{Workspace: "default", Name: "query-cluster"}},
			wantCode: "10209",
			wantHint: "does not match patch target",
		},
		{
			name:            "returns validation error when endpoint lookup fails",
			patch:           cluster("gpu-cluster", &v1.AcceleratorVirtualizationSpec{Enabled: false}),
			endpointErr:     errors.New("database error"),
			lookupEndpoints: true,
			wantCode:        "10209",
			wantHint:        "database error",
		},
		{
			name:            "rejects clearing accelerator virtualization with null while vGPU endpoint references cluster",
			patch:           cluster("gpu-cluster", nil),
			endpoints:       []v1.Endpoint{vGPUEndpoint},
			lookupEndpoints: true,
			wantCode:        "10211",
			wantHint:        "1 vGPU endpoint(s) still reference this cluster",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := storageMocks.NewMockStorage(t)
			if tt.resolved != nil {
				mockStorage.On("ListCluster", mock.MatchedBy(func(opt storage.ListOption) bool {
					return sameFilters(opt.Filters, queryParamsToFilters(tt.query))
				})).Return([]v1.Cluster{*tt.resolved}, nil).Once()
			}

			if tt.lookupEndpoints {
				identity := tt.patch.Metadata
				if tt.resolved != nil {
					identity = tt.resolved.Metadata
				}
				mockStorage.On("ListEndpoint", storage.ListOption{
					Filters: clusterEndpointReferenceFilters(identity.Workspace, identity.Name),
				}).Return(tt.endpoints, tt.endpointErr).Once()
			}

			err := validateClusterAcceleratorVirtualizationDisable(mockStorage, tt.patch, tt.query)
			if tt.wantCode == "" {
				assert.Nil(t, err)
			} else if assert.NotNil(t, err) {
				assert.Equal(t, tt.wantCode, err.Code)
				if tt.wantMessage != "" {
					assert.Contains(t, err.Message, tt.wantMessage)
				}
				if tt.wantHint != "" {
					assert.Contains(t, err.Hint, tt.wantHint)
				}
			}

			mockStorage.AssertExpectations(t)
			if !tt.lookupEndpoints {
				mockStorage.AssertNotCalled(t, "ListEndpoint", mock.Anything)
			}
		})
	}
}

func TestClusterValidationInput(t *testing.T) {
	t.Run("prepare", testPrepareClusterValidationInput)
	t.Run("build PATCH validation New", testBuildClusterPatchValidationNew)
}

func testPrepareClusterValidationInput(t *testing.T) {
	prepare := func(t *testing.T, current v1.Cluster, body string) *ValidationInput[v1.Cluster] {
		t.Helper()

		var payload map[string]json.RawMessage
		assert.NoError(t, json.Unmarshal([]byte(body), &payload))

		var patch v1.Cluster
		assert.NoError(t, json.Unmarshal([]byte(body), &patch))

		mockStorage := storageMocks.NewMockStorage(t)
		mockStorage.On("ListCluster", mock.MatchedBy(func(opt storage.ListOption) bool {
			return sameFilters(opt.Filters, queryParamsToFilters(url.Values{"id": []string{"eq.1"}}))
		})).Return([]v1.Cluster{current}, nil).Once()

		input := &ValidationInput[v1.Cluster]{
			Method:      http.MethodPatch,
			Body:        []byte(body),
			RawPayload:  payload,
			Patch:       patch,
			QueryParams: url.Values{"id": []string{"eq.1"}},
			Operation:   clusterValidationPatch,
		}

		validationErr := prepareClusterValidationInput(mockStorage, input)
		assert.Nil(t, validationErr)
		mockStorage.AssertExpectations(t)

		return input
	}

	sshCluster := func() v1.Cluster {
		return v1.Cluster{
			Metadata: &v1.Metadata{Workspace: "default", Name: "ssh"},
			Spec: &v1.ClusterSpec{
				Type: v1.SSHClusterType,
				Config: &v1.ClusterConfig{SSHConfig: &v1.RaySSHProvisionClusterConfig{
					Auth: v1.Auth{SSHPrivateKey: "current-private-key"},
				}},
			},
		}
	}

	tests := []struct {
		name    string
		current v1.Cluster
		body    string
		assert  func(*testing.T, *ValidationInput[v1.Cluster])
	}{
		{
			name: "creates a complete New cluster and preserves an empty kubeconfig",
			current: v1.Cluster{
				Metadata: &v1.Metadata{Workspace: "default", Name: "kubernetes"},
				Spec: &v1.ClusterSpec{
					Type:          v1.KubernetesClusterType,
					ImageRegistry: "current-registry",
					Version:       "v1.0.0",
					Config: &v1.ClusterConfig{KubernetesConfig: &v1.KubernetesClusterConfig{
						Kubeconfig: "current-kubeconfig",
					}},
				},
			},
			body: `{"spec":{"version":"v1.1.0","config":{"kubernetes_config":{"kubeconfig":""}}}}`,
			assert: func(t *testing.T, input *ValidationInput[v1.Cluster]) {
				assert.NotSame(t, input.Current, input.New)
				assert.Equal(t, "v1.0.0", input.Current.Spec.Version)
				assert.Equal(t, "current-kubeconfig", input.Current.Spec.Config.KubernetesConfig.Kubeconfig)
				assert.Equal(t, v1.KubernetesClusterType, input.New.Spec.Type)
				assert.Equal(t, "current-registry", input.New.Spec.ImageRegistry)
				assert.Equal(t, "v1.1.0", input.New.Spec.Version)
				assert.Equal(t, "current-kubeconfig", input.New.Spec.Config.KubernetesConfig.Kubeconfig)
			},
		},
		{
			name:    "preserves an empty SSH private key",
			current: sshCluster(),
			body:    `{"spec":{"config":{"ssh_config":{"auth":{"ssh_private_key":""}}}}}`,
			assert: func(t *testing.T, input *ValidationInput[v1.Cluster]) {
				assert.Equal(t, "current-private-key", input.Current.Spec.Config.SSHConfig.Auth.SSHPrivateKey)
				assert.Equal(t, "current-private-key", input.New.Spec.Config.SSHConfig.Auth.SSHPrivateKey)
			},
		},
		{
			name:    "preserves a null SSH private key",
			current: sshCluster(),
			body:    `{"spec":{"config":{"ssh_config":{"auth":{"ssh_private_key":null}}}}}`,
			assert: func(t *testing.T, input *ValidationInput[v1.Cluster]) {
				assert.Equal(t, "current-private-key", input.Current.Spec.Config.SSHConfig.Auth.SSHPrivateKey)
				assert.Equal(t, "current-private-key", input.New.Spec.Config.SSHConfig.Auth.SSHPrivateKey)
			},
		},
		{
			name: "retains an explicit config null in the raw update for configuration validation",
			current: v1.Cluster{
				Metadata: &v1.Metadata{Workspace: "default", Name: "kubernetes"},
				Spec:     &v1.ClusterSpec{Type: v1.KubernetesClusterType, Config: &v1.ClusterConfig{}},
			},
			body: `{"spec":{"config":null}}`,
			assert: func(t *testing.T, input *ValidationInput[v1.Cluster]) {
				update, err := parseClusterPatchConfigurationUpdatePayload(input.RawPayload)
				assert.NoError(t, err)
				assert.True(t, update.configCleared)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := prepare(t, tt.current, tt.body)
			assert.Equal(t, &tt.current, input.Current)
			tt.assert(t, input)
		})
	}
}

func testBuildClusterPatchValidationNew(t *testing.T) {
	tests := []struct {
		name    string
		current *v1.Cluster
		body    string
		wantErr bool
		assert  func(*testing.T, *v1.Cluster, *v1.Cluster)
	}{
		{
			name: "builds a complete spec from a sparse patch without mutating Current",
			body: `{"spec":{"version":"v1.1.0"}}`,
			assert: func(t *testing.T, current, next *v1.Cluster) {
				assert.NotSame(t, current, next)
				assert.Equal(t, "v1.0.0", current.Spec.Version)
				assert.Equal(t, "v1.1.0", next.Spec.Version)
				assert.Equal(t, v1.KubernetesClusterType, next.Spec.Type)
				assert.Equal(t, "current-registry", next.Spec.ImageRegistry)
			},
		},
		{
			name: "recursively merges sparse configuration and accelerator virtualization",
			current: &v1.Cluster{Spec: &v1.ClusterSpec{
				Type:          v1.KubernetesClusterType,
				ImageRegistry: "current-registry",
				Version:       "v1.0.0",
				Config: &v1.ClusterConfig{KubernetesConfig: &v1.KubernetesClusterConfig{
					Kubeconfig: "current-kubeconfig",
					Router:     v1.RouterSpec{AccessMode: v1.KubernetesAccessModeLoadBalancer, Replicas: 1},
				}},
				AcceleratorVirtualization: &v1.AcceleratorVirtualizationSpec{
					Enabled:     true,
					ConfigPatch: map[string]interface{}{"devicePlugin": map[string]interface{}{"enabled": true}},
				},
			}},
			body: `{"spec":{"config":{"kubernetes_config":{"router":{"replicas":3}}},"accelerator_virtualization":{"config_patch":{"devicePlugin":{"nvidiaDriverRoot":"/run/nvidia/driver"}}}}}`,
			assert: func(t *testing.T, current, next *v1.Cluster) {
				assert.Equal(t, "current-kubeconfig", next.Spec.Config.KubernetesConfig.Kubeconfig)
				assert.Equal(t, v1.KubernetesAccessModeLoadBalancer, next.Spec.Config.KubernetesConfig.Router.AccessMode)
				assert.Equal(t, 3, next.Spec.Config.KubernetesConfig.Router.Replicas)
				assert.True(t, next.Spec.AcceleratorVirtualization.Enabled)
				assert.Equal(t, true, next.Spec.AcceleratorVirtualization.ConfigPatch["devicePlugin"].(map[string]interface{})["enabled"])
				assert.Equal(t, "/run/nvidia/driver", next.Spec.AcceleratorVirtualization.ConfigPatch["devicePlugin"].(map[string]interface{})["nvidiaDriverRoot"])
				assert.Equal(t, 1, current.Spec.Config.KubernetesConfig.Router.Replicas)
			},
		},
		{
			name: "backfills a null masked kubeconfig",
			current: &v1.Cluster{Spec: &v1.ClusterSpec{Config: &v1.ClusterConfig{
				KubernetesConfig: &v1.KubernetesClusterConfig{Kubeconfig: "current-kubeconfig"},
			}}},
			body: `{"spec":{"config":{"kubernetes_config":{"kubeconfig":null}}}}`,
			assert: func(t *testing.T, current, next *v1.Cluster) {
				assert.Equal(t, "current-kubeconfig", next.Spec.Config.KubernetesConfig.Kubeconfig)
			},
		},
		{
			name: "backfills a missing masked kubeconfig while merging sibling configuration",
			current: &v1.Cluster{Spec: &v1.ClusterSpec{Config: &v1.ClusterConfig{
				KubernetesConfig: &v1.KubernetesClusterConfig{
					Kubeconfig: "current-kubeconfig",
					Router:     v1.RouterSpec{AccessMode: v1.KubernetesAccessModeLoadBalancer},
				},
			}}},
			body: `{"spec":{"config":{"kubernetes_config":{"router":{"replicas":2}}}}}`,
			assert: func(t *testing.T, current, next *v1.Cluster) {
				assert.Equal(t, "current-kubeconfig", next.Spec.Config.KubernetesConfig.Kubeconfig)
				assert.Equal(t, v1.KubernetesAccessModeLoadBalancer, next.Spec.Config.KubernetesConfig.Router.AccessMode)
				assert.Equal(t, 2, next.Spec.Config.KubernetesConfig.Router.Replicas)
			},
		},
		{
			name: "backfills an empty masked kubeconfig",
			current: &v1.Cluster{Spec: &v1.ClusterSpec{Config: &v1.ClusterConfig{
				KubernetesConfig: &v1.KubernetesClusterConfig{Kubeconfig: "current-kubeconfig"},
			}}},
			body: `{"spec":{"config":{"kubernetes_config":{"kubeconfig":""}}}}`,
			assert: func(t *testing.T, current, next *v1.Cluster) {
				assert.Equal(t, "current-kubeconfig", next.Spec.Config.KubernetesConfig.Kubeconfig)
			},
		},
		{
			name: "uses an explicit non-empty masked kubeconfig",
			current: &v1.Cluster{Spec: &v1.ClusterSpec{Config: &v1.ClusterConfig{
				KubernetesConfig: &v1.KubernetesClusterConfig{Kubeconfig: "current-kubeconfig"},
			}}},
			body: `{"spec":{"config":{"kubernetes_config":{"kubeconfig":"replacement-kubeconfig"}}}}`,
			assert: func(t *testing.T, current, next *v1.Cluster) {
				assert.Equal(t, "replacement-kubeconfig", next.Spec.Config.KubernetesConfig.Kubeconfig)
				assert.Equal(t, "current-kubeconfig", current.Spec.Config.KubernetesConfig.Kubeconfig)
			},
		},
		{
			name: "backfills a missing masked SSH private key",
			current: &v1.Cluster{Spec: &v1.ClusterSpec{Config: &v1.ClusterConfig{
				SSHConfig: &v1.RaySSHProvisionClusterConfig{Auth: v1.Auth{SSHPrivateKey: "current-private-key"}},
			}}},
			body: `{"spec":{"config":{"ssh_config":{"auth":{}}}}}`,
			assert: func(t *testing.T, current, next *v1.Cluster) {
				assert.Equal(t, "current-private-key", next.Spec.Config.SSHConfig.Auth.SSHPrivateKey)
			},
		},
		{
			name: "backfills an empty masked SSH private key",
			current: &v1.Cluster{Spec: &v1.ClusterSpec{Config: &v1.ClusterConfig{
				SSHConfig: &v1.RaySSHProvisionClusterConfig{Auth: v1.Auth{SSHPrivateKey: "current-private-key"}},
			}}},
			body: `{"spec":{"config":{"ssh_config":{"auth":{"ssh_private_key":""}}}}}`,
			assert: func(t *testing.T, current, next *v1.Cluster) {
				assert.Equal(t, "current-private-key", next.Spec.Config.SSHConfig.Auth.SSHPrivateKey)
			},
		},
		{
			name: "backfills a null masked SSH private key",
			current: &v1.Cluster{Spec: &v1.ClusterSpec{Config: &v1.ClusterConfig{
				SSHConfig: &v1.RaySSHProvisionClusterConfig{Auth: v1.Auth{SSHPrivateKey: "current-private-key"}},
			}}},
			body: `{"spec":{"config":{"ssh_config":{"auth":{"ssh_private_key":null}}}}}`,
			assert: func(t *testing.T, current, next *v1.Cluster) {
				assert.Equal(t, "current-private-key", next.Spec.Config.SSHConfig.Auth.SSHPrivateKey)
			},
		},
		{
			name: "uses an explicit non-empty masked SSH private key",
			current: &v1.Cluster{Spec: &v1.ClusterSpec{Config: &v1.ClusterConfig{
				SSHConfig: &v1.RaySSHProvisionClusterConfig{Auth: v1.Auth{SSHPrivateKey: "current-private-key"}},
			}}},
			body: `{"spec":{"config":{"ssh_config":{"auth":{"ssh_private_key":"replacement-private-key"}}}}}`,
			assert: func(t *testing.T, current, next *v1.Cluster) {
				assert.Equal(t, "replacement-private-key", next.Spec.Config.SSHConfig.Auth.SSHPrivateKey)
			},
		},
		{
			name: "replaces model cache arrays atomically",
			current: &v1.Cluster{Spec: &v1.ClusterSpec{Config: &v1.ClusterConfig{
				ModelCaches: []v1.ModelCache{{Name: "current-cache"}},
			}}},
			body: `{"spec":{"config":{"model_caches":[{"name":"replacement-cache"}]}}}`,
			assert: func(t *testing.T, current, next *v1.Cluster) {
				assert.Equal(t, []v1.ModelCache{{Name: "replacement-cache"}}, next.Spec.Config.ModelCaches)
				assert.Equal(t, []v1.ModelCache{{Name: "current-cache"}}, current.Spec.Config.ModelCaches)
			},
		},
		{
			name:    "rejects malformed PATCH payloads",
			body:    `[`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			current := tt.current
			if current == nil {
				current = &v1.Cluster{Spec: &v1.ClusterSpec{
					Type:          v1.KubernetesClusterType,
					ImageRegistry: "current-registry",
					Version:       "v1.0.0",
				}}
			}

			next, err := buildClusterPatchValidationNew(current, []byte(tt.body))
			if tt.wantErr {
				assert.Error(t, err)

				return
			}

			assert.NoError(t, err)
			tt.assert(t, current, next)
		})
	}
}

func testValidateClusterAcceleratorVirtualizationMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("rejects disable patch before proxy handler when vGPU endpoint references cluster", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		mockStorage.On("ListCluster", mock.Anything).Return([]v1.Cluster{{
			Metadata: &v1.Metadata{Workspace: "default", Name: "gpu-cluster"},
			Spec: &v1.ClusterSpec{AcceleratorVirtualization: &v1.AcceleratorVirtualizationSpec{
				Enabled: true,
			}},
		}}, nil).Once()
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

	t.Run("preserves accelerator virtualization when a sparse spec omits it", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		mockStorage.On("ListCluster", mock.Anything).Return([]v1.Cluster{{
			Metadata: &v1.Metadata{Workspace: "default", Name: "gpu-cluster"},
			Spec: &v1.ClusterSpec{
				Type:    v1.KubernetesClusterType,
				Version: "v1.0.0",
				AcceleratorVirtualization: &v1.AcceleratorVirtualizationSpec{
					Enabled: true,
				},
			},
		}}, nil).Once()
		proxyCalled := false
		router := gin.New()
		router.PATCH("/clusters", validateClusterAcceleratorVirtualization(mockStorage), func(c *gin.Context) {
			proxyCalled = true
			c.Status(http.StatusNoContent)
		})

		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPatch, "/clusters?id=eq.1", strings.NewReader(`{
			"spec": {"version": "v1.1.0"}
		}`)))

		assert.True(t, proxyCalled)
		assert.Equal(t, http.StatusNoContent, recorder.Code)
		mockStorage.AssertExpectations(t)
		mockStorage.AssertNotCalled(t, "ListEndpoint", mock.Anything)
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
		mockStorage.AssertNotCalled(t, "ListCluster", mock.Anything)
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

func TestValidateClusterConfiguration(t *testing.T) {
	t.Run("middleware", testValidateClusterConfigurationUpdateMiddleware)
	t.Run("parse PATCH update", testParseClusterPatchConfigurationUpdate)
	t.Run("initialized update", testValidateInitializedClusterConfigurationUpdate)
}

func testValidateClusterConfigurationUpdateMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("skips configuration validation for non-PATCH requests", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		proxyCalled := false
		router := gin.New()
		router.POST("/clusters", validateClusterConfigurationUpdate(mockStorage), func(c *gin.Context) {
			proxyCalled = true
			c.Status(http.StatusNoContent)
		})

		req := httptest.NewRequest(http.MethodPost, "/clusters", strings.NewReader(`{
			"spec": {"config": {"kubernetes_config": {"kubeconfig": []}}}
		}`))
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, req)

		assert.True(t, proxyCalled)
		assert.Equal(t, http.StatusNoContent, recorder.Code)
		mockStorage.AssertNotCalled(t, "ListCluster", mock.Anything)
	})

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
		router.PATCH("/clusters", validateClusterConfigurationUpdate(mockStorage), func(c *gin.Context) {
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
		router.PATCH("/clusters", validateClusterConfigurationUpdate(mockStorage), func(c *gin.Context) {
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

	t.Run("allows empty kubeconfig for an initialized Kubernetes cluster", func(t *testing.T) {
		for _, kubeconfig := range []string{`""`, "null"} {
			t.Run(kubeconfig, func(t *testing.T) {
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
				router.PATCH("/clusters", validateClusterConfigurationUpdate(mockStorage), func(c *gin.Context) {
					proxyCalled = true
					c.Status(http.StatusNoContent)
				})

				body := fmt.Sprintf(`{"spec":{"config":{"kubernetes_config":{"kubeconfig":%s}}}}`, kubeconfig)
				req := httptest.NewRequest(http.MethodPatch, "/clusters?id=eq.1", strings.NewReader(body))
				recorder := httptest.NewRecorder()

				router.ServeHTTP(recorder, req)

				assert.True(t, proxyCalled)
				assert.Equal(t, http.StatusNoContent, recorder.Code)
				mockStorage.AssertExpectations(t)
			})
		}
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
		router.PATCH("/clusters", validateClusterConfigurationUpdate(mockStorage), func(c *gin.Context) {
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
		router.PATCH("/clusters", validateClusterConfigurationUpdate(mockStorage), func(c *gin.Context) {
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

	t.Run("rejects clearing Kubernetes config on an initialized cluster", func(t *testing.T) {
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
		router.PATCH("/clusters", validateClusterConfigurationUpdate(mockStorage), func(c *gin.Context) {
			proxyCalled = true
			c.Status(http.StatusNoContent)
		})

		req := httptest.NewRequest(http.MethodPatch, "/clusters?id=eq.1", strings.NewReader(`{
			"spec": {"config": {"kubernetes_config": null}}
		}`))
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, req)

		assert.False(t, proxyCalled)
		assert.Equal(t, http.StatusBadRequest, recorder.Code)
		assert.Contains(t, recorder.Body.String(), `"code":"10209"`)
		assert.Contains(t, recorder.Body.String(), "kubernetes_config cannot be cleared")
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
		router.PATCH("/clusters", validateClusterConfigurationUpdate(mockStorage), func(c *gin.Context) {
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

	t.Run("allows empty SSH private key for an initialized cluster", func(t *testing.T) {
		for _, sshPrivateKey := range []string{`""`, "null"} {
			t.Run(sshPrivateKey, func(t *testing.T) {
				mockStorage := storageMocks.NewMockStorage(t)
				query := url.Values{"id": []string{"eq.1"}}
				mockStorage.On("ListCluster", mock.MatchedBy(func(opt storage.ListOption) bool {
					return sameFilters(opt.Filters, queryParamsToFilters(query))
				})).Return([]v1.Cluster{{
					Spec:   &v1.ClusterSpec{Type: v1.SSHClusterType},
					Status: &v1.ClusterStatus{Initialized: true},
				}}, nil).Once()

				proxyCalled := false
				router := gin.New()
				router.PATCH("/clusters", validateClusterConfigurationUpdate(mockStorage), func(c *gin.Context) {
					proxyCalled = true
					c.Status(http.StatusNoContent)
				})

				body := fmt.Sprintf(`{"spec":{"config":{"ssh_config":{"auth":{"ssh_private_key":%s}}}}}`, sshPrivateKey)
				req := httptest.NewRequest(http.MethodPatch, "/clusters?id=eq.1", strings.NewReader(body))
				recorder := httptest.NewRecorder()

				router.ServeHTTP(recorder, req)

				assert.True(t, proxyCalled)
				assert.Equal(t, http.StatusNoContent, recorder.Code)
				mockStorage.AssertExpectations(t)
			})
		}
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
		router.PATCH("/clusters", validateClusterConfigurationUpdate(mockStorage), func(c *gin.Context) {
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
		router.PATCH("/clusters", validateClusterConfigurationUpdate(mockStorage), func(c *gin.Context) {
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

	t.Run("rejects clearing SSH config on an initialized cluster", func(t *testing.T) {
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
		}}, nil).Once()

		proxyCalled := false
		router := gin.New()
		router.PATCH("/clusters", validateClusterConfigurationUpdate(mockStorage), func(c *gin.Context) {
			proxyCalled = true
			c.Status(http.StatusNoContent)
		})

		req := httptest.NewRequest(http.MethodPatch, "/clusters?id=eq.1", strings.NewReader(`{
			"spec": {"config": {"ssh_config": null}}
		}`))
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, req)

		assert.False(t, proxyCalled)
		assert.Equal(t, http.StatusBadRequest, recorder.Code)
		assert.Contains(t, recorder.Body.String(), `"code":"10209"`)
		assert.Contains(t, recorder.Body.String(), "ssh_config cannot be cleared")
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
		router.PATCH("/clusters", validateClusterConfigurationUpdate(mockStorage), func(c *gin.Context) {
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
		router.PATCH("/clusters", validateClusterConfigurationUpdate(mockStorage), func(c *gin.Context) {
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

	t.Run("restores request body and content length", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		body := `{"metadata":{"name":"cluster"}}`
		originalBody := &trackingReadCloser{Reader: strings.NewReader(body)}
		router := gin.New()
		router.PATCH("/clusters", validateClusterConfigurationUpdate(mockStorage), func(c *gin.Context) {
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

func TestValidateClusterVersion(t *testing.T) {
	t.Run("middleware", testValidateClusterVersionUpdateMiddleware)
	t.Run("not downgrade", testValidateClusterVersionNotDowngrade)
}

func testValidateClusterVersionUpdateMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("skips version validation for a configuration-only patch", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		proxyCalled := false
		router := gin.New()
		router.PATCH("/clusters", validateClusterVersionUpdate(mockStorage), func(c *gin.Context) {
			proxyCalled = true
			c.Status(http.StatusNoContent)
		})

		req := httptest.NewRequest(http.MethodPatch, "/clusters?id=eq.1", strings.NewReader(`{
			"spec": {"config": {"kubernetes_config": {"kubeconfig": "updated-kubeconfig"}}}
		}`))
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, req)

		assert.True(t, proxyCalled)
		assert.Equal(t, http.StatusNoContent, recorder.Code)
		mockStorage.AssertNotCalled(t, "ListCluster", mock.Anything)
	})

	t.Run("rejects malformed configuration before resolving version update", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		proxyCalled := false
		router := gin.New()
		router.PATCH(
			"/clusters",
			validateClusterVersionUpdate(mockStorage),
			validateClusterConfigurationUpdate(mockStorage),
			func(c *gin.Context) {
				proxyCalled = true
				c.Status(http.StatusNoContent)
			},
		)

		req := httptest.NewRequest(http.MethodPatch, "/clusters?id=eq.1", strings.NewReader(`{
			"spec": {
				"version": "v1.2.0",
				"config": {"kubernetes_config": {"kubeconfig": []}}
			}
		}`))
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, req)

		assert.False(t, proxyCalled)
		assert.Equal(t, http.StatusBadRequest, recorder.Code)
		assert.Contains(t, recorder.Body.String(), `"code":"10209"`)
		assert.Contains(t, recorder.Body.String(), "invalid cluster payload")
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

func testParseClusterPatchConfigurationUpdate(t *testing.T) {
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

func testValidateInitializedClusterConfigurationUpdate(t *testing.T) {
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
	updatedCluster := func(t *testing.T, current *v1.Cluster, update clusterPatchConfigurationUpdate) *v1.Cluster {
		t.Helper()

		data, err := json.Marshal(current)
		assert.NoError(t, err)

		var next v1.Cluster
		assert.NoError(t, json.Unmarshal(data, &next))
		if next.Spec == nil {
			next.Spec = &v1.ClusterSpec{}
		}

		if update.imageRegistrySet {
			next.Spec.ImageRegistry = update.imageRegistry
		}

		if update.kubeconfigSet {
			if next.Spec.Config == nil {
				next.Spec.Config = &v1.ClusterConfig{}
			}
			if next.Spec.Config.KubernetesConfig == nil {
				next.Spec.Config.KubernetesConfig = &v1.KubernetesClusterConfig{}
			}
			next.Spec.Config.KubernetesConfig.Kubeconfig = update.kubeconfig
		}

		if update.sshPrivateKeySet {
			if next.Spec.Config == nil {
				next.Spec.Config = &v1.ClusterConfig{}
			}
			if next.Spec.Config.SSHConfig == nil {
				next.Spec.Config.SSHConfig = &v1.RaySSHProvisionClusterConfig{}
			}
			next.Spec.Config.SSHConfig.Auth.SSHPrivateKey = update.sshPrivateKey
		}

		return &next
	}

	validKubeconfig := testEncodedKubeconfig("https://api.example.test:6443", "old-token")
	initializedSSHCluster := func() *v1.Cluster {
		return &v1.Cluster{
			Spec:   &v1.ClusterSpec{Type: v1.SSHClusterType},
			Status: &v1.ClusterStatus{Initialized: true},
		}
	}

	tests := []struct {
		name            string
		current         *v1.Cluster
		update          clusterPatchConfigurationUpdate
		wantErrContains string
	}{
		{
			name:    "allows image registry changes before initialization",
			current: &v1.Cluster{Spec: &v1.ClusterSpec{ImageRegistry: "current"}, Status: &v1.ClusterStatus{Initialized: false}},
			update:  clusterPatchConfigurationUpdate{imageRegistry: "replacement", imageRegistrySet: true},
		},
		{
			name:    "allows an unchanged image registry",
			current: &v1.Cluster{Spec: &v1.ClusterSpec{ImageRegistry: "current"}, Status: &v1.ClusterStatus{Initialized: true}},
			update:  clusterPatchConfigurationUpdate{imageRegistry: "current", imageRegistrySet: true},
		},
		{
			name:            "rejects image registry changes after initialization",
			current:         &v1.Cluster{Spec: &v1.ClusterSpec{ImageRegistry: "current"}, Status: &v1.ClusterStatus{Initialized: true}},
			update:          clusterPatchConfigurationUpdate{imageRegistry: "replacement", imageRegistrySet: true},
			wantErrContains: "image registry cannot be changed",
		},
		{
			name:            "rejects a blank updated kubeconfig",
			current:         initializedKubernetesCluster(validKubeconfig),
			update:          clusterPatchConfigurationUpdate{kubeconfig: " ", kubeconfigSet: true},
			wantErrContains: "failed to parse updated kubeconfig",
		},
		{
			name:            "rejects a missing current Kubernetes config",
			current:         &v1.Cluster{Spec: &v1.ClusterSpec{Type: v1.KubernetesClusterType}, Status: &v1.ClusterStatus{Initialized: true}},
			update:          clusterPatchConfigurationUpdate{kubeconfig: testEncodedKubeconfig("https://api.example.test:6443", "new-token"), kubeconfigSet: true},
			wantErrContains: "failed to read current kubeconfig",
		},
		{
			name:            "rejects an invalid current kubeconfig",
			current:         initializedKubernetesCluster(base64.StdEncoding.EncodeToString([]byte("not a kubeconfig"))),
			update:          clusterPatchConfigurationUpdate{kubeconfig: testEncodedKubeconfig("https://api.example.test:6443", "new-token"), kubeconfigSet: true},
			wantErrContains: "failed to parse current kubeconfig",
		},
		{
			name:            "rejects an invalid updated kubeconfig",
			current:         initializedKubernetesCluster(validKubeconfig),
			update:          clusterPatchConfigurationUpdate{kubeconfig: "not-base64", kubeconfigSet: true},
			wantErrContains: "failed to parse updated kubeconfig",
		},
		{
			name:            "rejects a kubeconfig for a different Kubernetes API server",
			current:         initializedKubernetesCluster(validKubeconfig),
			update:          clusterPatchConfigurationUpdate{kubeconfig: testEncodedKubeconfig("https://other-api.example.test:6443", "new-token"), kubeconfigSet: true},
			wantErrContains: "current Kubernetes API server",
		},
		{
			name:    "allows same-host Kubernetes credential rotation",
			current: initializedKubernetesCluster(validKubeconfig),
			update:  clusterPatchConfigurationUpdate{kubeconfig: testEncodedKubeconfig("https://api.example.test:6443", "new-token"), kubeconfigSet: true},
		},
		{
			name:    "allows empty Kubernetes credential backfill",
			current: initializedKubernetesCluster(validKubeconfig),
			update:  clusterPatchConfigurationUpdate{kubeconfigSet: true},
		},
		{
			name:            "rejects a blank SSH private key",
			current:         initializedSSHCluster(),
			update:          clusterPatchConfigurationUpdate{sshPrivateKey: " ", sshPrivateKeySet: true},
			wantErrContains: "SSH private key must be base64 encoded",
		},
		{
			name:            "rejects an invalid SSH private key",
			current:         initializedSSHCluster(),
			update:          clusterPatchConfigurationUpdate{sshPrivateKey: "not-base64", sshPrivateKeySet: true},
			wantErrContains: "SSH private key must be base64 encoded",
		},
		{
			name:    "allows an SSH private key rotation",
			current: initializedSSHCluster(),
			update:  clusterPatchConfigurationUpdate{sshPrivateKey: base64.StdEncoding.EncodeToString([]byte("rotated-private-key")), sshPrivateKeySet: true},
		},
		{
			name:    "allows an empty SSH private key backfill",
			current: initializedSSHCluster(),
			update:  clusterPatchConfigurationUpdate{sshPrivateKeySet: true},
		},
		{
			name:            "rejects clearing the entire config",
			current:         initializedKubernetesCluster(validKubeconfig),
			update:          clusterPatchConfigurationUpdate{configCleared: true},
			wantErrContains: "config cannot be cleared",
		},
		{
			name:            "rejects clearing Kubernetes config",
			current:         initializedKubernetesCluster(validKubeconfig),
			update:          clusterPatchConfigurationUpdate{kubernetesConfigCleared: true},
			wantErrContains: "kubernetes_config cannot be cleared",
		},
		{
			name:            "rejects clearing SSH config",
			current:         initializedSSHCluster(),
			update:          clusterPatchConfigurationUpdate{sshConfigCleared: true},
			wantErrContains: "ssh_config cannot be cleared",
		},
		{
			name:            "rejects clearing SSH auth",
			current:         initializedSSHCluster(),
			update:          clusterPatchConfigurationUpdate{sshAuthCleared: true},
			wantErrContains: "SSH auth cannot be cleared",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateInitializedClusterConfigurationUpdate(
				tt.current,
				updatedCluster(t, tt.current, tt.update),
				tt.update,
			)

			if tt.wantErrContains == "" {
				assert.NoError(t, err)

				return
			}
			assert.ErrorContains(t, err, tt.wantErrContains)
		})
	}
}

type trackingReadCloser struct {
	*strings.Reader
	closed bool
}

func (r *trackingReadCloser) Close() error {
	r.closed = true

	return nil
}

func testValidateClusterVersionNotDowngrade(t *testing.T) {
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
