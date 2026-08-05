package proxies

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/admission"
	"github.com/neutree-ai/neutree/pkg/storage"
)

func TestB0RoutesRegisterAdmissionDescriptors(t *testing.T) {
	testCases := []struct {
		name     string
		register func(*gin.RouterGroup, []gin.HandlerFunc, *Dependencies) error
		resource any
	}{
		{
			name:     "api keys",
			register: RegisterAPIKeyRoutes,
			resource: admission.NewResource[v1.ApiKey](storage.API_KEY_TABLE),
		},
		{
			name:     "role assignments",
			register: RegisterRoleAssignmentRoutes,
			resource: admission.NewResource[v1.RoleAssignment](storage.ROLE_ASSIGNMENT_TABLE),
		},
		{
			name:     "engines",
			register: RegisterEngineRoutes,
			resource: admission.NewResource[v1.Engine](storage.ENGINE_TABLE),
		},
		{
			name:     "oem configs",
			register: RegisterOEMConfigRoutes,
			resource: admission.NewResource[v1.OEMConfig](oemConfigTable),
		},
		{
			name:     "external endpoints",
			register: RegisterExternalEndpointRoutes,
			resource: admission.NewResource[v1.ExternalEndpoint](storage.EXTERNAL_ENDPOINT_TABLE),
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			registry := admission.NewRegistry()
			router := gin.New()

			require.NoError(t, testCase.register(router.Group("/resource"), nil, &Dependencies{Admission: registry}))
			require.ErrorContains(t, registry.RegisterResource(testCase.resource), "already registered")
		})
	}
}

func TestRegisterEngineRoutesRunsCreateAndPatchAdmissionChains(t *testing.T) {
	registry := admission.NewRegistry()
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, http.MethodGet, request.Method)
		require.Equal(t, "/engines", request.URL.Path)
		require.Equal(t, "eq.1", request.URL.Query().Get("id"))
		require.Equal(t, "*", request.URL.Query().Get("select"))
		require.Equal(t, "Bearer postgrest-token", request.Header.Get("Authorization"))
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write([]byte(`[{"id":1,"metadata":{"name":"engine","workspace":"default"},"spec":{}}]`))
		require.NoError(t, err)
	}))
	t.Cleanup(upstream.Close)

	router := gin.New()
	router.Use(func(context *gin.Context) { context.Set("postgrest_token", "postgrest-token") })
	require.NoError(t, RegisterEngineRoutes(router.Group(""), nil, &Dependencies{
		Admission:        registry,
		StorageAccessURL: upstream.URL,
	}))
	require.NoError(t, registry.RegisterHook(admission.NewResource[v1.Engine](storage.ENGINE_TABLE), admission.ValidateCreate(
		admission.HookMeta{Name: "test.create", Order: 1}, 91101,
		func(admission.RequestContext, v1.Engine) error {
			return &admission.Error{Code: 91101, Message: "create runner invoked"}
		},
	)))
	require.NoError(t, registry.RegisterHook(admission.NewResource[v1.Engine](storage.ENGINE_TABLE), admission.ValidateUpdate(
		admission.HookMeta{Name: "test.update", Order: 1}, 91102,
		func(admission.RequestContext, v1.Engine, v1.Engine) error {
			return &admission.Error{Code: 91102, Message: "patch runner invoked"}
		},
	)))
	require.NoError(t, registry.Seal())

	post := httptest.NewRecorder()
	postRequest := httptest.NewRequest(
		http.MethodPost,
		"/engines",
		strings.NewReader(`{"metadata":{"name":"engine","workspace":"default"},"spec":{}}`),
	)
	postRequest.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(post, postRequest)
	require.Equal(t, http.StatusBadRequest, post.Code)
	require.JSONEq(t, `{"code":91101,"message":"create runner invoked"}`, post.Body.String())

	patch := httptest.NewRecorder()
	patchRequest := httptest.NewRequest(http.MethodPatch, "/engines?id=eq.1", strings.NewReader(`{"spec":{}}`))
	patchRequest.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(patch, patchRequest)
	require.Equal(t, http.StatusBadRequest, patch.Code)
	require.JSONEq(t, `{"code":91102,"message":"patch runner invoked"}`, patch.Body.String())
}

func TestRegisterEngineRoutesKeepsProxyBehaviorWithoutAdmission(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, http.MethodGet, request.Method)
		require.Equal(t, "/engines", request.URL.Path)
		writer.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)

	router := gin.New()
	require.NoError(t, RegisterEngineRoutes(router.Group(""), nil, &Dependencies{StorageAccessURL: upstream.URL}))

	response := newCloseNotifyRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/engines", nil))
	require.Equal(t, http.StatusNoContent, response.ResponseRecorder.Code)
}

func TestRegisterAPIKeyRoutesDoesNotRegisterPost(t *testing.T) {
	router := gin.New()
	require.NoError(t, RegisterAPIKeyRoutes(router.Group(""), nil, &Dependencies{Admission: admission.NewRegistry()}))

	for _, route := range router.Routes() {
		if route.Path == "/api_keys" && route.Method == http.MethodPost {
			t.Fatal("POST /api_keys must not be registered")
		}
	}
}

func TestRegisterOEMConfigRoutesKeepsGetOutsideMiddlewares(t *testing.T) {
	registry := admission.NewRegistry()
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			writer.Header().Set("Content-Type", "application/json")
			_, err := writer.Write([]byte(`[{"id":1,"metadata":{"name":"oem","workspace":"default"},"spec":{}}]`))
			require.NoError(t, err)
		case http.MethodPost, http.MethodPatch:
			writer.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected upstream request method %s", request.Method)
		}
	}))
	t.Cleanup(upstream.Close)

	middlewareCalls := 0
	router := gin.New()
	middlewares := []gin.HandlerFunc{func(*gin.Context) { middlewareCalls++ }}
	require.NoError(t, RegisterOEMConfigRoutes(router.Group(""), middlewares, &Dependencies{
		Admission:        registry,
		StorageAccessURL: upstream.URL,
	}))
	require.NoError(t, registry.Seal())

	getResponse := newCloseNotifyRecorder()
	router.ServeHTTP(getResponse, httptest.NewRequest(http.MethodGet, "/oem_configs", nil))
	require.Equal(t, http.StatusOK, getResponse.ResponseRecorder.Code)
	require.Zero(t, middlewareCalls)

	postResponse := newCloseNotifyRecorder()
	postRequest := httptest.NewRequest(
		http.MethodPost,
		"/oem_configs",
		strings.NewReader(`{"metadata":{"name":"oem","workspace":"default"},"spec":{}}`),
	)
	postRequest.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(postResponse, postRequest)
	require.Equal(t, http.StatusNoContent, postResponse.ResponseRecorder.Code)
	require.Equal(t, 1, middlewareCalls)

	patchResponse := newCloseNotifyRecorder()
	patchRequest := httptest.NewRequest(http.MethodPatch, "/oem_configs?id=eq.1", strings.NewReader(`{"spec":{}}`))
	patchRequest.Header.Set("Authorization", "Bearer postgrest-token")
	patchRequest.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(patchResponse, patchRequest)
	require.Equal(t, http.StatusNoContent, patchResponse.ResponseRecorder.Code)
	require.Equal(t, 2, middlewareCalls)
}
