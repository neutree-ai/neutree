package proxies

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/admission"
	"github.com/neutree-ai/neutree/pkg/storage"
	storageMocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func TestRegisterImageRegistryRoutesRejectsInvalidURLHostOnCreate(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var upstreamCalled atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalled.Store(true)
		w.WriteHeader(http.StatusCreated)
	}))
	defer upstream.Close()

	registry := admission.NewRegistry()
	router := gin.New()
	require.NoError(t, RegisterImageRegistryRoutes(router.Group("/api/v1"), nil, &Dependencies{
		StorageAccessURL: upstream.URL,
		Admission:        registry,
	}))
	require.NoError(t, registry.Seal())

	body := strings.NewReader(`{
		"api_version":"v1",
		"kind":"ImageRegistry",
		"metadata":{"name":"invalid-registry","workspace":"default"},
		"spec":{"url":"https://index.docker<>.io","repository":"neutree"}
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/image_registries", body)
	req.Header.Set("Content-Type", "application/json")

	recorder := newCloseNotifyRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusBadRequest, recorder.ResponseRecorder.Code)
	assert.Contains(t, recorder.ResponseRecorder.Body.String(), "invalid image registry url")
	assert.False(t, upstreamCalled.Load(), "invalid image registry should not be forwarded to PostgREST")
}

func TestRegisterImageRegistryRoutesForwardsValidURLOnCreate(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var upstreamCalled atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled.Store(true)
		assert.Equal(t, "/image_registries", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		w.WriteHeader(http.StatusCreated)
	}))
	defer upstream.Close()

	router := gin.New()
	RegisterImageRegistryRoutes(router.Group("/api/v1"), nil, &Dependencies{
		StorageAccessURL: upstream.URL,
	})

	body := strings.NewReader(`{
		"api_version":"v1",
		"kind":"ImageRegistry",
		"metadata":{"name":"valid-registry","workspace":"default"},
		"spec":{"url":"https://registry.example.com:5000","repository":"neutree"}
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/image_registries", body)
	req.Header.Set("Content-Type", "application/json")

	recorder := newCloseNotifyRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusCreated, recorder.ResponseRecorder.Code)
	assert.True(t, upstreamCalled.Load(), "valid image registry should be forwarded to PostgREST")
}

func TestRegisterImageRegistryRoutesRejectsInvalidURLHostOnPatch(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var upstreamCalled atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`[{"id":118,"metadata":{"name":"invalid-registry","workspace":"default"},"spec":{"url":"https://registry.example.com","repository":"neutree"}}]`))
			require.NoError(t, err)
			return
		}
		upstreamCalled.Store(true)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	registry := admission.NewRegistry()
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("postgrest_token", "image-registry-test-token") })
	require.NoError(t, RegisterImageRegistryRoutes(router.Group("/api/v1"), nil, &Dependencies{
		StorageAccessURL: upstream.URL,
		Admission:        registry,
	}))
	require.NoError(t, registry.Seal())

	body := strings.NewReader(`{"spec":{"url":"https://index.docker<>.io"}}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/image_registries?id=eq.118", body)
	req.Header.Set("Content-Type", "application/json")

	recorder := newCloseNotifyRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusBadRequest, recorder.ResponseRecorder.Code)
	assert.Contains(t, recorder.ResponseRecorder.Body.String(), "invalid image registry url")
	assert.False(t, upstreamCalled.Load(), "invalid image registry patch should not be forwarded to PostgREST")
}

func TestRegisterImageRegistryRoutesForwardsValidURLOnPatch(t *testing.T) {
	gin.SetMode(gin.TestMode)

	storageMock := storageMocks.NewMockStorage(t)
	storageMock.EXPECT().
		GenericQuery(storage.IMAGE_REGISTRY_TABLE, "spec", mock.Anything, mock.Anything).
		Run(func(_ string, _ string, _ []storage.Filter, result interface{}) {
			resources := result.(*[]map[string]interface{})
			*resources = []map[string]interface{}{
				{
					"spec": map[string]interface{}{
						"authconfig": map[string]interface{}{
							"username": "existing-user",
							"password": "existing-password",
						},
					},
				},
			}
		}).
		Return(nil)

	var upstreamCalled atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`[{"id":118,"metadata":{"name":"registry","workspace":"default"},"spec":{"url":"https://registry.example.com","repository":"neutree"}}]`))
			require.NoError(t, err)
			return
		}
		upstreamCalled.Store(true)
		assert.Equal(t, "/image_registries", r.URL.Path)
		assert.Equal(t, "id=eq.118", r.URL.RawQuery)
		assert.Equal(t, http.MethodPatch, r.Method)

		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		assert.Contains(t, string(body), `"url":"https://registry.example.com:5000"`)

		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	registry := admission.NewRegistry()
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("postgrest_token", "image-registry-test-token") })
	require.NoError(t, RegisterImageRegistryRoutes(router.Group("/api/v1"), nil, &Dependencies{
		StorageAccessURL: upstream.URL,
		Storage:          storageMock,
		Admission:        registry,
	}))
	var admittedOld, admittedCandidate v1.ImageRegistry
	require.NoError(t, registry.RegisterHook(imageRegistryAdmissionResource, admission.ValidateUpdate(
		admission.HookMeta{Name: "community.image-registry.capture-update", Order: 900}, 91904,
		func(_ admission.RequestContext, old, candidate v1.ImageRegistry) error {
			admittedOld, admittedCandidate = old, candidate
			return nil
		},
	)))
	require.NoError(t, registry.Seal())

	body := strings.NewReader(`{"spec":{"url":"https://registry.example.com:5000"}}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/image_registries?id=eq.118", body)
	req.Header.Set("Content-Type", "application/json")

	recorder := newCloseNotifyRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusNoContent, recorder.ResponseRecorder.Code)
	assert.True(t, upstreamCalled.Load(), "valid image registry patch should be forwarded to PostgREST")
	assert.Equal(t, "https://registry.example.com", admittedOld.Spec.URL)
	assert.Equal(t, "https://registry.example.com:5000", admittedCandidate.Spec.URL)
}

func TestRegisterImageRegistryRoutesPreservesCredentialsForCLIPatchWithEmptyAuthConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)

	storageMock := storageMocks.NewMockStorage(t)
	storageMock.EXPECT().
		GenericQuery(storage.IMAGE_REGISTRY_TABLE, "spec", mock.Anything, mock.Anything).
		Run(func(_ string, _ string, _ []storage.Filter, result interface{}) {
			resources := result.(*[]map[string]interface{})
			*resources = []map[string]interface{}{
				{
					"spec": map[string]interface{}{
						"authconfig": map[string]interface{}{
							"username": "existing-user",
							"password": "existing-password",
						},
					},
				},
			}
		}).
		Return(nil)

	var forwardedBody map[string]interface{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`[{"id":118,"metadata":{"name":"registry","workspace":"default"},"spec":{"url":"https://registry.example.com","repository":"neutree"}}]`))
			require.NoError(t, err)
			return
		}

		require.Equal(t, http.MethodPatch, r.Method)
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &forwardedBody))
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)

	registry := admission.NewRegistry()
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("postgrest_token", "image-registry-test-token") })
	require.NoError(t, RegisterImageRegistryRoutes(router.Group("/api/v1"), nil, &Dependencies{
		StorageAccessURL: upstream.URL,
		Storage:          storageMock,
		Admission:        registry,
	}))
	require.NoError(t, registry.Seal())

	body := strings.NewReader(`{
		"api_version":"v1",
		"kind":"ImageRegistry",
		"metadata":{"name":"registry","workspace":"default"},
		"spec":{
			"url":"https://registry.example.com:5000",
			"repository":"updated-repository",
			"authconfig":{}
		}
	}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/image_registries?id=eq.118", body)
	req.Header.Set("Content-Type", "application/json")

	recorder := newCloseNotifyRecorder()
	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusNoContent, recorder.ResponseRecorder.Code)
	require.Equal(t, "updated-repository", forwardedBody["spec"].(map[string]interface{})["repository"])
	require.Equal(t, map[string]interface{}{
		"username": "existing-user",
		"password": "existing-password",
	}, forwardedBody["spec"].(map[string]interface{})["authconfig"])
}

func TestImageRegistryURLAdmissionHooksRejectInvalidCandidates(t *testing.T) {
	registry := admission.NewRegistry()
	require.NoError(t, registerImageRegistryAdmission(&Dependencies{Admission: registry}))
	require.NoError(t, registry.Seal())

	candidate := v1.ImageRegistry{Spec: &v1.ImageRegistrySpec{URL: "https://index.docker<>.io"}}
	for _, testCase := range []struct {
		name      string
		operation admission.Operation
		hookName  string
	}{
		{name: "create", operation: admission.Create, hookName: "community.image-registry.url.create"},
		{name: "update", operation: admission.Update, hookName: "community.image-registry.url.update"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			chain, err := registry.Chain(imageRegistryAdmissionResource, testCase.operation)
			require.NoError(t, err)
			require.Len(t, chain.Hooks(), 1)
			require.Equal(t, testCase.hookName, chain.Hooks()[0].Name)

			_, err = chain.Run(admission.RequestContext{Context: context.Background()}, v1.ImageRegistry{}, candidate)
			require.Error(t, err)
			require.Contains(t, err.Error(), "invalid image registry url")
		})
	}
}

func TestImageRegistryAdmissionPreservesLegacyMalformedBodyResponse(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		method string
		path   string
	}{
		{name: "create", method: http.MethodPost, path: "/api/v1/image_registries"},
		{name: "update", method: http.MethodPatch, path: "/api/v1/image_registries?id=eq.118"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			upstreamCalled := false
			upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				upstreamCalled = true
			}))
			t.Cleanup(upstream.Close)

			registry := admission.NewRegistry()
			router := gin.New()
			router.Use(func(c *gin.Context) { c.Set("postgrest_token", "image-registry-test-token") })
			require.NoError(t, RegisterImageRegistryRoutes(router.Group("/api/v1"), nil, &Dependencies{
				Admission:        registry,
				StorageAccessURL: upstream.URL,
			}))
			require.NoError(t, registry.Seal())

			recorder := newCloseNotifyRecorder()
			request := httptest.NewRequest(testCase.method, testCase.path, strings.NewReader(`{"spec":`))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, request)

			require.Equal(t, http.StatusBadRequest, recorder.ResponseRecorder.Code)
			require.JSONEq(t, `{"error":"failed to parse image registry: unexpected end of JSON input"}`, recorder.ResponseRecorder.Body.String())
			require.False(t, upstreamCalled)
		})
	}
}

func TestImageRegistryAdmissionPreservesLegacyBodyReadFailureResponse(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		method string
		path   string
	}{
		{name: "create", method: http.MethodPost, path: "/api/v1/image_registries"},
		{name: "update", method: http.MethodPatch, path: "/api/v1/image_registries?id=eq.118"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			registry := admission.NewRegistry()
			router := gin.New()
			router.Use(func(c *gin.Context) { c.Set("postgrest_token", "image-registry-test-token") })
			require.NoError(t, RegisterImageRegistryRoutes(router.Group("/api/v1"), nil, &Dependencies{Admission: registry}))
			require.NoError(t, registry.Seal())

			recorder := newCloseNotifyRecorder()
			request := httptest.NewRequest(testCase.method, testCase.path, nil)
			request.Body = &failingPatchBody{readErr: errors.New("body failed")}
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, request)

			require.Equal(t, http.StatusBadRequest, recorder.ResponseRecorder.Code)
			require.JSONEq(t, `{"error":"failed to read request body: body failed"}`, recorder.ResponseRecorder.Body.String())
		})
	}
}

func TestValidateImageRegistryDeletion(t *testing.T) {
	tests := []struct {
		name         string
		workspace    string
		registryName string
		clusterCount int
		queryError   error
		expectError  bool
		expectedCode int
		expectedHint string
	}{
		{
			name:         "no dependencies - deletion allowed",
			workspace:    "default",
			registryName: "my-registry",
			clusterCount: 0,
			queryError:   nil,
			expectError:  false,
		},
		{
			name:         "has dependencies - deletion blocked",
			workspace:    "default",
			registryName: "my-registry",
			clusterCount: 3,
			queryError:   nil,
			expectError:  true,
			expectedCode: 10127,
			expectedHint: "3 cluster(s) still reference this image registry",
		},
		{
			name:         "query error",
			workspace:    "default",
			registryName: "my-registry",
			clusterCount: 0,
			queryError:   errors.New("database error"),
			expectError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := storageMocks.NewMockStorage(t)

			mockStorage.On("Count",
				storage.CLUSTERS_TABLE,
				[]storage.Filter{
					{Column: "metadata->>workspace", Operator: "eq", Value: tt.workspace},
					{Column: "spec->>image_registry", Operator: "eq", Value: tt.registryName},
				},
			).Return(tt.clusterCount, tt.queryError)

			err := validateImageRegistryDeleteDependencies(mockStorage, v1.ImageRegistry{Metadata: &v1.Metadata{Workspace: tt.workspace, Name: tt.registryName}})

			if tt.expectError {
				assert.Error(t, err)

				if tt.queryError == nil {
					var deletionErr *admission.Error
					ok := errors.As(err, &deletionErr)
					assert.True(t, ok, "error should be admission.Error")
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
