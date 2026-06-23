package proxies

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
	storageMocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func TestValidateEndpointAcceleratorVirtualizationBody(t *testing.T) {
	t.Run("allows non vGPU endpoint resources", func(t *testing.T) {
		err := validateEndpointAcceleratorVirtualizationBody([]byte(`{
			"metadata": {"name": "endpoint", "workspace": "default"},
			"spec": {
				"cluster": "cluster",
				"resources": {
					"gpu": "1",
					"accelerator": {
						"type": "nvidia_gpu",
						"product": "Tesla-T4"
					}
				}
			}
		}`))

		assert.Nil(t, err)
	})

	t.Run("allows raw accelerator keys without virtualization fields", func(t *testing.T) {
		err := validateEndpointAcceleratorVirtualizationBody([]byte(`{
			"metadata": {"name": "endpoint", "workspace": "default"},
			"spec": {
				"cluster": "cluster",
				"resources": {
					"gpu": "1",
					"accelerator": {
						"type": "nvidia_gpu",
						"product": "Tesla-T4",
						"nvidia.com/gpucores": "50"
					}
				}
			}
		}`))

		assert.Nil(t, err)
	})

	t.Run("rejects vGPU endpoint without product", func(t *testing.T) {
		err := validateEndpointAcceleratorVirtualizationBody([]byte(`{
			"metadata": {"name": "endpoint", "workspace": "default"},
			"spec": {
				"cluster": "cluster",
				"resources": {
					"gpu": "1",
					"accelerator": {
						"type": "nvidia_gpu",
						"virtualization.memory_mib": "8192"
					}
				}
			}
		}`))

		assert.NotNil(t, err)
		assert.Equal(t, "10218", err.Code)
	})

	t.Run("rejects mutually exclusive memory fields", func(t *testing.T) {
		err := validateEndpointAcceleratorVirtualizationBody([]byte(`{
			"metadata": {"name": "endpoint", "workspace": "default"},
			"spec": {
				"cluster": "cluster",
				"resources": {
					"gpu": "1",
					"accelerator": {
						"type": "nvidia_gpu",
						"product": "Tesla-T4",
						"virtualization.memory_mib": "8192",
						"virtualization.memory_percent": "50"
					}
				}
			}
		}`))

		assert.NotNil(t, err)
		assert.Equal(t, "10219", err.Code)
	})

	t.Run("rejects invalid vGPU numeric resources", func(t *testing.T) {
		err := validateEndpointAcceleratorVirtualizationBody([]byte(`{
			"metadata": {"name": "endpoint", "workspace": "default"},
			"spec": {
				"cluster": "cluster",
				"resources": {
					"gpu": "1",
					"accelerator": {
						"type": "nvidia_gpu",
						"product": "Tesla-T4",
						"virtualization.memory_mib": "8192",
						"virtualization.core_percent": "101"
					}
				}
			}
		}`))

		assert.NotNil(t, err)
		assert.Equal(t, "10216", err.Code)
		assert.Contains(t, err.Hint, "virtualization.core_percent")
	})

	t.Run("rejects fractional vGPU memory resource", func(t *testing.T) {
		err := validateEndpointAcceleratorVirtualizationBody([]byte(`{
			"metadata": {"name": "endpoint", "workspace": "default"},
			"spec": {
				"cluster": "cluster",
				"resources": {
					"gpu": "1",
					"accelerator": {
						"type": "nvidia_gpu",
						"product": "Tesla-T4",
						"virtualization.memory_mib": "8192.5",
						"virtualization.core_percent": "50"
					}
				}
			}
		}`))

		assert.NotNil(t, err)
		assert.Equal(t, "10216", err.Code)
		assert.Contains(t, err.Hint, "virtualization.memory_mib")
		assert.Contains(t, err.Hint, "positive integer")
	})

	t.Run("rejects fractional vGPU core resource", func(t *testing.T) {
		err := validateEndpointAcceleratorVirtualizationBody([]byte(`{
			"metadata": {"name": "endpoint", "workspace": "default"},
			"spec": {
				"cluster": "cluster",
				"resources": {
					"gpu": "1",
					"accelerator": {
						"type": "nvidia_gpu",
						"product": "Tesla-T4",
						"virtualization.memory_mib": "8192",
						"virtualization.core_percent": "50.5"
					}
				}
			}
		}`))

		assert.NotNil(t, err)
		assert.Equal(t, "10216", err.Code)
		assert.Contains(t, err.Hint, "virtualization.core_percent")
		assert.Contains(t, err.Hint, "positive integer")
	})

	t.Run("allows vGPU endpoint resource shape without cluster availability lookup", func(t *testing.T) {
		err := validateEndpointAcceleratorVirtualizationBody([]byte(`{
			"metadata": {"name": "endpoint", "workspace": "default"},
			"spec": {
				"cluster": "cluster",
				"resources": {
					"gpu": "1",
					"accelerator": {
						"type": "nvidia_gpu",
						"product": "Tesla-T4",
						"virtualization.memory_mib": "8192",
						"virtualization.core_percent": "50"
					}
				}
			}
		}`))

		assert.Nil(t, err)
	})

	t.Run("skips patch that does not touch resources", func(t *testing.T) {
		err := validateEndpointAcceleratorVirtualizationBody([]byte(`{
			"spec": {
				"replicas": {"num": 2}
			}
		}`))

		assert.Nil(t, err)
	})
}

func TestValidateEndpointAcceleratorVirtualizationRequestRunsBodyAndPreflight(t *testing.T) {
	storageMock := storageMocks.NewMockStorage(t)
	storageMock.EXPECT().
		ListCluster(matchListOption(clusterLookupFilters("k8s-cluster", "default"))).
		Return([]v1.Cluster{endpointValidationCluster(true, v1.ComponentPhaseReady, 32768, 200)}, nil)

	err := validateEndpointAcceleratorVirtualizationRequest(
		storageMock,
		http.MethodPost,
		nil,
		[]byte(endpointValidationVGPUPayload("k8s-cluster")),
	)

	assert.Nil(t, err)
}

func TestRegisterEndpointRoutesRejectsVGPUCreateWhenClusterVirtualizationUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name          string
		clusters      []v1.Cluster
		expectedBody  string
		expectedError string
	}{
		{
			name: "cluster disabled",
			clusters: []v1.Cluster{
				endpointValidationCluster(false, v1.ComponentPhaseReady, 32768, 200),
			},
			expectedBody:  "accelerator virtualization",
			expectedError: "not ready",
		},
		{
			name: "component not ready",
			clusters: []v1.Cluster{
				endpointValidationCluster(true, v1.ComponentPhaseNotReady, 32768, 200),
			},
			expectedBody:  "accelerator virtualization",
			expectedError: "not ready",
		},
		{
			name: "component status missing",
			clusters: []v1.Cluster{
				endpointValidationClusterMissingComponentStatus(),
			},
			expectedBody:  "accelerator virtualization",
			expectedError: "component status is missing",
		},
		{
			name: "non Kubernetes cluster",
			clusters: []v1.Cluster{
				endpointValidationClusterWithType(v1.SSHClusterType, true, v1.ComponentPhaseReady),
			},
			expectedBody:  "accelerator virtualization",
			expectedError: "only supported for kubernetes clusters",
		},
		{
			name:          "cluster not found",
			clusters:      []v1.Cluster{},
			expectedBody:  "cluster",
			expectedError: "not found",
		},
		{
			name: "cluster lookup ambiguous",
			clusters: []v1.Cluster{
				endpointValidationCluster(true, v1.ComponentPhaseReady, 32768, 200),
				endpointValidationCluster(true, v1.ComponentPhaseReady, 32768, 200),
			},
			expectedBody:  "cluster",
			expectedError: "multiple",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storageMock := storageMocks.NewMockStorage(t)
			storageMock.EXPECT().
				ListCluster(matchListOption(clusterLookupFilters("k8s-cluster", "default"))).
				Return(tt.clusters, nil)

			router, upstreamCalled := setupEndpointValidationRoute(t, storageMock, http.StatusCreated)
			recorder := performEndpointValidationRequest(router, http.MethodPost, "/api/v1/endpoints", endpointValidationVGPUPayload("k8s-cluster"))

			assert.Equal(t, http.StatusBadRequest, recorder.ResponseRecorder.Code)
			assert.Contains(t, recorder.ResponseRecorder.Body.String(), tt.expectedBody)
			assert.Contains(t, recorder.ResponseRecorder.Body.String(), tt.expectedError)
			assert.False(t, upstreamCalled.Load(), "invalid vGPU endpoint should not be forwarded to PostgREST")
		})
	}
}

func TestRegisterEndpointRoutesForwardsVGPUCreateWhenClusterVirtualizationReady(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name    string
		cluster v1.Cluster
		body    string
	}{
		{
			name:    "different product is available",
			cluster: endpointValidationClusterWithProduct("Other-GPU", true, v1.ComponentPhaseReady, 32768, 200),
			body:    endpointValidationVGPUPayload("k8s-cluster"),
		},
		{
			name:    "memory_mib availability is low",
			cluster: endpointValidationCluster(true, v1.ComponentPhaseReady, 512, 200),
			body:    endpointValidationVGPUPayload("k8s-cluster"),
		},
		{
			name:    "core availability is low",
			cluster: endpointValidationCluster(true, v1.ComponentPhaseReady, 32768, 5),
			body:    endpointValidationVGPUPayload("k8s-cluster"),
		},
		{
			name:    "product metadata is missing",
			cluster: endpointValidationClusterWithoutProductMetadata(true, v1.ComponentPhaseReady, 32768, 200),
			body:    endpointValidationVGPUPercentPayload("k8s-cluster"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storageMock := storageMocks.NewMockStorage(t)
			storageMock.EXPECT().
				ListCluster(matchListOption(clusterLookupFilters("k8s-cluster", "default"))).
				Return([]v1.Cluster{tt.cluster}, nil)

			router, upstreamCalled := setupEndpointValidationRoute(t, storageMock, http.StatusCreated)
			recorder := performEndpointValidationRequest(router, http.MethodPost, "/api/v1/endpoints", tt.body)

			assert.Equal(t, http.StatusCreated, recorder.ResponseRecorder.Code)
			assert.True(t, upstreamCalled.Load(), "NEU-476 only checks cluster virtualization readiness, not capacity")
		})
	}
}

func TestRegisterEndpointRoutesHidesClusterLookupErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)

	storageMock := storageMocks.NewMockStorage(t)
	storageMock.EXPECT().
		ListCluster(matchListOption(clusterLookupFilters("k8s-cluster", "default"))).
		Return(nil, errors.New("postgres://internal-secret"))

	router, upstreamCalled := setupEndpointValidationRoute(t, storageMock, http.StatusCreated)
	recorder := performEndpointValidationRequest(router, http.MethodPost, "/api/v1/endpoints", endpointValidationVGPUPayload("k8s-cluster"))

	assert.Equal(t, http.StatusServiceUnavailable, recorder.ResponseRecorder.Code)
	assert.Contains(t, recorder.ResponseRecorder.Body.String(), "failed to look up cluster")
	assert.NotContains(t, recorder.ResponseRecorder.Body.String(), "internal-secret")
	assert.False(t, upstreamCalled.Load(), "cluster lookup errors should not be forwarded to PostgREST")
}

func TestRegisterEndpointRoutesForwardsVGPUCreateWhenClusterVirtualizationIsReady(t *testing.T) {
	gin.SetMode(gin.TestMode)

	storageMock := storageMocks.NewMockStorage(t)
	storageMock.EXPECT().
		ListCluster(matchListOption(clusterLookupFilters("k8s-cluster", "default"))).
		Return([]v1.Cluster{endpointValidationCluster(true, v1.ComponentPhaseReady, 32768, 200)}, nil)

	router, upstreamCalled := setupEndpointValidationRoute(t, storageMock, http.StatusCreated)
	recorder := performEndpointValidationRequest(router, http.MethodPost, "/api/v1/endpoints", endpointValidationVGPUPayload("k8s-cluster"))

	assert.Equal(t, http.StatusCreated, recorder.ResponseRecorder.Code)
	assert.True(t, upstreamCalled.Load(), "valid vGPU endpoint should be forwarded to PostgREST")
}

func TestRegisterEndpointRoutesForwardsOriginalVGPURequestBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{
			name:   "post",
			method: http.MethodPost,
			path:   "/api/v1/endpoints",
			body:   endpointValidationVGPUPayload("k8s-cluster"),
		},
		{
			name:   "patch",
			method: http.MethodPatch,
			path:   "/api/v1/endpoints?metadata->>workspace=eq.default&id=eq.118",
			body:   endpointValidationVGPUResourcePatchPayloadWithCluster("k8s-cluster"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storageMock := storageMocks.NewMockStorage(t)
			storageMock.EXPECT().
				ListCluster(matchListOption(clusterLookupFilters("k8s-cluster", "default"))).
				Return([]v1.Cluster{endpointValidationCluster(true, v1.ComponentPhaseReady, 32768, 200)}, nil)

			router, upstreamCalled, upstreamBody := setupEndpointValidationRouteWithBodyCapture(t, storageMock, http.StatusCreated)
			recorder := performEndpointValidationRequest(router, tt.method, tt.path, tt.body)

			assert.Equal(t, http.StatusCreated, recorder.ResponseRecorder.Code)
			assert.True(t, upstreamCalled.Load(), "valid vGPU request should be forwarded to PostgREST")
			assert.JSONEq(t, tt.body, upstreamBody.Load().(string))
		})
	}
}

func TestRegisterEndpointRoutesResolvesExistingEndpointForVGPUResourcePatch(t *testing.T) {
	gin.SetMode(gin.TestMode)

	storageMock := storageMocks.NewMockStorage(t)
	storageMock.EXPECT().
		ListEndpoint(matchListOption([]storage.Filter{
			{Column: "metadata->>name", Operator: "eq", Value: "endpoint"},
			{Column: "metadata->>workspace", Operator: "eq", Value: "default"},
		})).
		Return([]v1.Endpoint{
			{
				Metadata: &v1.Metadata{Name: "endpoint", Workspace: "default"},
				Spec:     &v1.EndpointSpec{Cluster: "k8s-cluster"},
			},
		}, nil)
	storageMock.EXPECT().
		ListCluster(matchListOption(clusterLookupFilters("k8s-cluster", "default"))).
		Return([]v1.Cluster{endpointValidationCluster(true, v1.ComponentPhaseReady, 32768, 200)}, nil)

	router, upstreamCalled := setupEndpointValidationRoute(t, storageMock, http.StatusNoContent)
	recorder := performEndpointValidationRequest(
		router,
		http.MethodPatch,
		"/api/v1/endpoints?metadata->>name=eq.endpoint&metadata->>workspace=eq.default&select=*",
		endpointValidationVGPUResourcePatchPayload(),
	)

	assert.Equal(t, http.StatusNoContent, recorder.ResponseRecorder.Code)
	assert.True(t, upstreamCalled.Load(), "valid vGPU patch should be forwarded to PostgREST")
}

func TestRegisterEndpointRoutesSkipsPreflightWhenPatchOmitsVGPUResourceKeys(t *testing.T) {
	gin.SetMode(gin.TestMode)

	storageMock := storageMocks.NewMockStorage(t)

	router, upstreamCalled := setupEndpointValidationRoute(t, storageMock, http.StatusNoContent)
	recorder := performEndpointValidationRequest(
		router,
		http.MethodPatch,
		"/api/v1/endpoints?metadata->>name=eq.endpoint&metadata->>workspace=eq.default",
		`{"spec":{"cluster":"unsupported-cluster"}}`,
	)

	assert.Equal(t, http.StatusNoContent, recorder.ResponseRecorder.Code)
	assert.True(t, upstreamCalled.Load(), "PATCH without vGPU resource keys should follow the design and skip preflight")
}

func TestRegisterEndpointRoutesRejectsVGPUResourcePatchWhenExistingEndpointLookupIsAmbiguous(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name      string
		endpoints []v1.Endpoint
		expected  string
	}{
		{
			name:      "not found",
			endpoints: nil,
			expected:  "not found",
		},
		{
			name: "multiple",
			endpoints: []v1.Endpoint{
				{Metadata: &v1.Metadata{Name: "endpoint", Workspace: "default"}, Spec: &v1.EndpointSpec{Cluster: "a"}},
				{Metadata: &v1.Metadata{Name: "endpoint", Workspace: "default"}, Spec: &v1.EndpointSpec{Cluster: "b"}},
			},
			expected: "multiple",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storageMock := storageMocks.NewMockStorage(t)
			storageMock.EXPECT().
				ListEndpoint(matchListOption([]storage.Filter{
					{Column: "metadata->>name", Operator: "eq", Value: "endpoint"},
					{Column: "metadata->>workspace", Operator: "eq", Value: "default"},
				})).
				Return(tt.endpoints, nil)

			router, upstreamCalled := setupEndpointValidationRoute(t, storageMock, http.StatusNoContent)
			recorder := performEndpointValidationRequest(
				router,
				http.MethodPatch,
				"/api/v1/endpoints?metadata->>name=eq.endpoint&metadata->>workspace=eq.default",
				endpointValidationVGPUResourcePatchPayload(),
			)

			assert.Equal(t, http.StatusBadRequest, recorder.ResponseRecorder.Code)
			assert.Contains(t, recorder.ResponseRecorder.Body.String(), tt.expected)
			assert.False(t, upstreamCalled.Load(), "ambiguous vGPU patch should not be forwarded to PostgREST")
		})
	}
}

func TestRegisterEndpointRoutesHidesEndpointLookupErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)

	storageMock := storageMocks.NewMockStorage(t)
	storageMock.EXPECT().
		ListEndpoint(matchListOption([]storage.Filter{
			{Column: "metadata->>name", Operator: "eq", Value: "endpoint"},
			{Column: "metadata->>workspace", Operator: "eq", Value: "default"},
		})).
		Return(nil, errors.New("postgres://endpoint-secret"))

	router, upstreamCalled := setupEndpointValidationRoute(t, storageMock, http.StatusNoContent)
	recorder := performEndpointValidationRequest(
		router,
		http.MethodPatch,
		"/api/v1/endpoints?metadata->>name=eq.endpoint&metadata->>workspace=eq.default",
		endpointValidationVGPUResourcePatchPayload(),
	)

	assert.Equal(t, http.StatusServiceUnavailable, recorder.ResponseRecorder.Code)
	assert.Contains(t, recorder.ResponseRecorder.Body.String(), "failed to look up endpoint")
	assert.NotContains(t, recorder.ResponseRecorder.Body.String(), "endpoint-secret")
	assert.False(t, upstreamCalled.Load(), "endpoint lookup errors should not be forwarded to PostgREST")
}

func TestRegisterEndpointRoutesResolvesExistingEndpointForVGPUResourcePatchWithIDFilter(t *testing.T) {
	gin.SetMode(gin.TestMode)

	storageMock := storageMocks.NewMockStorage(t)
	storageMock.EXPECT().
		ListEndpoint(matchListOption([]storage.Filter{
			{Column: "id", Operator: "eq", Value: "118"},
		})).
		Return([]v1.Endpoint{
			{
				Metadata: &v1.Metadata{Name: "endpoint", Workspace: "default"},
				Spec:     &v1.EndpointSpec{Cluster: "k8s-cluster"},
			},
		}, nil)
	storageMock.EXPECT().
		ListCluster(matchListOption(clusterLookupFilters("k8s-cluster", "default"))).
		Return([]v1.Cluster{endpointValidationCluster(true, v1.ComponentPhaseReady, 32768, 200)}, nil)

	router, upstreamCalled := setupEndpointValidationRoute(t, storageMock, http.StatusNoContent)
	recorder := performEndpointValidationRequest(
		router,
		http.MethodPatch,
		"/api/v1/endpoints?id=eq.118",
		endpointValidationVGPUResourcePatchPayload(),
	)

	assert.Equal(t, http.StatusNoContent, recorder.ResponseRecorder.Code)
	assert.True(t, upstreamCalled.Load(), "vGPU resource PATCH can resolve existing endpoint from regular query filters")
}

func TestRegisterEndpointRoutesUsesExistingEndpointWorkspaceForVGPUResourcePatch(t *testing.T) {
	gin.SetMode(gin.TestMode)

	storageMock := storageMocks.NewMockStorage(t)
	storageMock.EXPECT().
		ListEndpoint(matchListOption([]storage.Filter{
			{Column: "id", Operator: "eq", Value: "118"},
		})).
		Return([]v1.Endpoint{
			{
				Metadata: &v1.Metadata{Name: "endpoint", Workspace: "workspace-a"},
				Spec:     &v1.EndpointSpec{Cluster: "old-cluster"},
			},
		}, nil)
	storageMock.EXPECT().
		ListCluster(matchListOption(clusterLookupFilters("k8s-cluster", "workspace-a"))).
		Return([]v1.Cluster{endpointValidationClusterInWorkspace("workspace-a", true, v1.ComponentPhaseReady, 32768, 200)}, nil)

	router, upstreamCalled := setupEndpointValidationRoute(t, storageMock, http.StatusNoContent)
	recorder := performEndpointValidationRequest(
		router,
		http.MethodPatch,
		"/api/v1/endpoints?id=eq.118",
		endpointValidationVGPUResourcePatchPayloadWithCluster("k8s-cluster"),
	)

	assert.Equal(t, http.StatusNoContent, recorder.ResponseRecorder.Code)
	assert.True(t, upstreamCalled.Load(), "vGPU resource PATCH should use existing endpoint workspace when patch omits metadata.workspace")
}

func TestRegisterEndpointRoutesSkipsClusterPreflightForNonVGPUAndSoftDeletePatch(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name string
		body string
	}{
		{
			name: "non vGPU patch",
			body: `{"spec":{"replicas":{"num":2}}}`,
		},
		{
			name: "soft delete patch",
			body: `{"metadata":{"name":"endpoint","workspace":"default","deletion_timestamp":"2026-06-23T08:00:00Z"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router, upstreamCalled := setupEndpointValidationRoute(t, nil, http.StatusNoContent)
			recorder := performEndpointValidationRequest(router, http.MethodPatch, "/api/v1/endpoints?id=eq.118", tt.body)

			assert.Equal(t, http.StatusNoContent, recorder.ResponseRecorder.Code)
			assert.True(t, upstreamCalled.Load(), "non-vGPU and soft-delete patches should be forwarded")
		})
	}
}

func setupEndpointValidationRoute(t *testing.T, store storage.Storage, upstreamStatus int) (*gin.Engine, *atomic.Bool) {
	return setupEndpointValidationRouteWithUser(t, store, upstreamStatus, "test-user-uuid")
}

func setupEndpointValidationRouteWithoutUser(t *testing.T, store storage.Storage, upstreamStatus int) (*gin.Engine, *atomic.Bool) {
	return setupEndpointValidationRouteWithUser(t, store, upstreamStatus, "")
}

func setupEndpointValidationRouteWithUser(t *testing.T, store storage.Storage, upstreamStatus int, userID string) (*gin.Engine, *atomic.Bool) {
	t.Helper()

	router, upstreamCalled, _ := setupEndpointValidationRouteWithBodyCaptureAndUser(t, store, upstreamStatus, userID)

	return router, upstreamCalled
}

func setupEndpointValidationRouteWithBodyCapture(t *testing.T, store storage.Storage, upstreamStatus int) (*gin.Engine, *atomic.Bool, *atomic.Value) {
	t.Helper()

	return setupEndpointValidationRouteWithBodyCaptureAndUser(t, store, upstreamStatus, "test-user-uuid")
}

func setupEndpointValidationRouteWithBodyCaptureAndUser(t *testing.T, store storage.Storage, upstreamStatus int, userID string) (*gin.Engine, *atomic.Bool, *atomic.Value) {
	t.Helper()

	var upstreamCalled atomic.Bool
	var upstreamBody atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled.Store(true)
		assert.Equal(t, "/endpoints", r.URL.Path)
		if r.Method == http.MethodPost || r.Method == http.MethodPatch {
			body, err := io.ReadAll(r.Body)
			assert.NoError(t, err)
			upstreamBody.Store(string(body))
		}
		w.WriteHeader(upstreamStatus)
	}))
	t.Cleanup(upstream.Close)

	router := gin.New()
	middlewares := []gin.HandlerFunc{}
	if userID != "" {
		middlewares = append(middlewares, func(c *gin.Context) {
			c.Set("user_id", userID)
			c.Next()
		})
	}

	RegisterEndpointRoutes(router.Group("/api/v1"), middlewares, &Dependencies{
		StorageAccessURL: upstream.URL,
		Storage:          store,
	})

	return router, &upstreamCalled, &upstreamBody
}

func performEndpointValidationRequest(router *gin.Engine, method, path, body string) *closeNotifyRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	recorder := newCloseNotifyRecorder()
	router.ServeHTTP(recorder, req)

	return recorder
}

func endpointValidationVGPUPayload(cluster string) string {
	return `{
		"api_version": "v1",
		"kind": "Endpoint",
		"metadata": {"name": "endpoint", "workspace": "default"},
		"spec": {
			"cluster": "` + cluster + `",
			"model": {"registry": "dummy", "name": "dummy", "version": "v0"},
			"engine": {"engine": "vllm", "version": "v0.12.0"},
			"resources": {
				"gpu": "1",
				"accelerator": {
					"type": "nvidia_gpu",
					"product": "Tesla-T4",
					"virtualization.memory_mib": "1024",
					"virtualization.core_percent": "10"
				}
			},
			"replicas": {"num": 1}
		}
	}`
}

func endpointValidationVGPUPercentPayload(cluster string) string {
	return `{
		"api_version": "v1",
		"kind": "Endpoint",
		"metadata": {"name": "endpoint", "workspace": "default"},
		"spec": {
			"cluster": "` + cluster + `",
			"resources": {
				"gpu": "1",
				"accelerator": {
					"type": "nvidia_gpu",
					"product": "Tesla-T4",
					"virtualization.memory_percent": "50",
					"virtualization.core_percent": "10"
				}
			}
		}
	}`
}

func endpointValidationVGPUResourcePatchPayload() string {
	return `{
		"spec": {
			"resources": {
				"gpu": "1",
				"accelerator": {
					"type": "nvidia_gpu",
					"product": "Tesla-T4",
					"virtualization.memory_mib": "1024",
					"virtualization.core_percent": "10"
				}
			}
		}
	}`
}

func endpointValidationVGPUResourcePatchPayloadWithCluster(cluster string) string {
	return `{
		"spec": {
			"cluster": "` + cluster + `",
			"resources": {
				"gpu": "1",
				"accelerator": {
					"type": "nvidia_gpu",
					"product": "Tesla-T4",
					"virtualization.memory_mib": "1024",
					"virtualization.core_percent": "10"
				}
			}
		}
	}`
}

func endpointValidationCluster(enabled bool, phase v1.ComponentPhase, memoryMiB, coreUnits float64) v1.Cluster {
	return endpointValidationClusterInWorkspace("default", enabled, phase, memoryMiB, coreUnits)
}

func endpointValidationClusterInWorkspace(workspace string, enabled bool, phase v1.ComponentPhase, memoryMiB, coreUnits float64) v1.Cluster {
	cluster := endpointValidationClusterWithProduct("Tesla-T4", enabled, phase, memoryMiB, coreUnits)
	cluster.Metadata.Workspace = workspace

	return cluster
}

func endpointValidationClusterWithoutProductMetadata(enabled bool, phase v1.ComponentPhase, memoryMiB, coreUnits float64) v1.Cluster {
	cluster := endpointValidationCluster(enabled, phase, memoryMiB, coreUnits)
	cluster.Status.ResourceInfo.AcceleratorMetadata = nil

	return cluster
}

func endpointValidationClusterWithProduct(product string, enabled bool, phase v1.ComponentPhase, memoryMiB, coreUnits float64) v1.Cluster {
	return v1.Cluster{
		Metadata: &v1.Metadata{Name: "k8s-cluster", Workspace: "default"},
		Spec: &v1.ClusterSpec{
			Type:                      v1.KubernetesClusterType,
			AcceleratorVirtualization: &v1.AcceleratorVirtualizationSpec{Enabled: enabled},
		},
		Status: &v1.ClusterStatus{
			ComponentStatus: map[string]*v1.ComponentStatus{
				v1.ComponentStatusAcceleratorVirtualizationKey: {
					Phase:   phase,
					Reason:  "HAMiNotReady",
					Message: "HAMi device plugin is not ready",
				},
			},
			ResourceInfo: &v1.ClusterResources{
				AcceleratorMetadata: map[v1.AcceleratorType]*v1.AcceleratorMetadata{
					v1.AcceleratorTypeNVIDIAGPU: {
						Products: map[v1.AcceleratorProduct]*v1.AcceleratorProductMetadata{
							v1.AcceleratorProduct(product): {MemoryTotalMiB: 16384},
						},
					},
				},
				ResourceStatus: v1.ResourceStatus{
					Available: &v1.ResourceInfo{
						AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
							v1.AcceleratorTypeNVIDIAGPU: {
								Products: map[v1.AcceleratorProduct]*v1.AcceleratorProductResource{
									v1.AcceleratorProduct(product): {
										Quantity: 1,
										Virtualization: &v1.AcceleratorVirtualizationResource{
											MemoryMiB: memoryMiB,
											CoreUnits: coreUnits,
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

func endpointValidationClusterMissingComponentStatus() v1.Cluster {
	cluster := endpointValidationCluster(true, v1.ComponentPhaseReady, 32768, 200)
	cluster.Status.ComponentStatus = nil

	return cluster
}

func endpointValidationClusterWithType(clusterType string, enabled bool, phase v1.ComponentPhase) v1.Cluster {
	cluster := endpointValidationCluster(enabled, phase, 32768, 200)
	cluster.Spec.Type = clusterType

	return cluster
}

func clusterLookupFilters(cluster, workspace string) []storage.Filter {
	return []storage.Filter{
		{Column: "metadata->name", Operator: "eq", Value: strconv.Quote(cluster)},
		{Column: "metadata->workspace", Operator: "eq", Value: strconv.Quote(workspace)},
	}
}

func matchListOption(filters []storage.Filter) interface{} {
	return mock.MatchedBy(func(option storage.ListOption) bool {
		return filtersEqualUnordered(filters, option.Filters)
	})
}

func filtersEqualUnordered(expected, actual []storage.Filter) bool {
	if len(expected) != len(actual) {
		return false
	}

	unmatched := make([]storage.Filter, len(actual))
	copy(unmatched, actual)

	for _, expectedFilter := range expected {
		found := false
		for i, actualFilter := range unmatched {
			if reflect.DeepEqual(expectedFilter, actualFilter) {
				unmatched = append(unmatched[:i], unmatched[i+1:]...)
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	return len(unmatched) == 0
}
