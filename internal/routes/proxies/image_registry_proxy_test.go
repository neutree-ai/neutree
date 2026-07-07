package proxies

import (
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

	"github.com/neutree-ai/neutree/internal/middleware"
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

	router := gin.New()
	RegisterImageRegistryRoutes(router.Group("/api/v1"), nil, &Dependencies{
		StorageAccessURL: upstream.URL,
	})

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
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalled.Store(true)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	router := gin.New()
	RegisterImageRegistryRoutes(router.Group("/api/v1"), nil, &Dependencies{
		StorageAccessURL: upstream.URL,
	})

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

	router := gin.New()
	RegisterImageRegistryRoutes(router.Group("/api/v1"), nil, &Dependencies{
		StorageAccessURL: upstream.URL,
		Storage:          storageMock,
	})

	body := strings.NewReader(`{"spec":{"url":"https://registry.example.com:5000"}}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/image_registries?id=eq.118", body)
	req.Header.Set("Content-Type", "application/json")

	recorder := newCloseNotifyRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusNoContent, recorder.ResponseRecorder.Code)
	assert.True(t, upstreamCalled.Load(), "valid image registry patch should be forwarded to PostgREST")
}

func TestValidateImageRegistryDeletion(t *testing.T) {
	tests := []struct {
		name         string
		workspace    string
		registryName string
		clusterCount int
		queryError   error
		expectError  bool
		expectedCode string
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
			expectedCode: "10127",
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

			validator := validateImageRegistryDeletion(mockStorage)
			err := validator(tt.workspace, tt.registryName)

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
