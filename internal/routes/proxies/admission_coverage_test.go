package proxies

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/admission"
)

const (
	coverageCreateCode = 91901
	coverageUpdateCode = 91902
)

type admissionCoverageResource struct {
	name          string
	path          string
	hasCreate     bool
	register      func(*gin.RouterGroup, []gin.HandlerFunc, *Dependencies) error
	assertTyped   func(*admission.Registry) error
	registerHooks func(*admission.Registry) error
}

func TestDefaultRESTWriteResourcesRegisterTypedAdmissionDescriptors(t *testing.T) {
	for _, resource := range defaultRESTAdmissionCoverageResources() {
		t.Run(resource.name, func(t *testing.T) {
			registry := admission.NewRegistry()
			require.NoError(t, resource.register(gin.New().Group("/resource"), nil, &Dependencies{Admission: registry}))
			require.ErrorContains(t, resource.assertTyped(registry), "already registered")
		})
	}
}

func TestDefaultRESTWriteResourcesRunAdmissionChains(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			writer.Header().Set("Content-Type", "application/json")
			_, err := writer.Write([]byte(`[{}]`))
			require.NoError(t, err)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)

	for _, resource := range defaultRESTAdmissionCoverageResources() {
		t.Run(resource.name, func(t *testing.T) {
			registry := admission.NewRegistry()
			router := gin.New()
			router.Use(func(context *gin.Context) { context.Set("postgrest_token", "coverage-token") })
			require.NoError(t, resource.register(router.Group(""), nil, &Dependencies{
				Admission:        registry,
				StorageAccessURL: upstream.URL,
			}))
			require.NoError(t, resource.registerHooks(registry))
			require.NoError(t, registry.Seal())

			if resource.hasCreate {
				response := newCloseNotifyRecorder()
				request := httptest.NewRequest(http.MethodPost, resource.path, strings.NewReader(`{}`))
				request.Header.Set("Content-Type", "application/json")
				router.ServeHTTP(response, request)
				require.Equal(t, http.StatusBadRequest, response.ResponseRecorder.Code)
				require.JSONEq(t, `{"code":91901,"message":"create admission runner invoked"}`, response.ResponseRecorder.Body.String())
			}

			response := newCloseNotifyRecorder()
			request := httptest.NewRequest(http.MethodPatch, resource.path+"?id=eq.1", strings.NewReader(`{}`))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(response, request)
			require.Equal(t, http.StatusBadRequest, response.ResponseRecorder.Code)
			require.JSONEq(t, `{"code":91902,"message":"patch admission runner invoked"}`, response.ResponseRecorder.Body.String())
		})
	}
}

func TestExternalEndpointConnectivityPostIsNotAResourceWrite(t *testing.T) {
	router := gin.New()
	require.NoError(t, RegisterExternalEndpointRoutes(router.Group(""), nil, &Dependencies{Admission: admission.NewRegistry()}))

	for _, route := range router.Routes() {
		if route.Method == http.MethodPost && route.Path == "/external_endpoints/test_connectivity" {
			return
		}
	}
	t.Fatal("POST /external_endpoints/test_connectivity must remain an explicit non-resource route")
}

func TestLegacyCreateAdmissionRoutesPreserveEmptyAndUnknownBodies(t *testing.T) {
	legacyResources := []struct {
		admissionCoverageResource
		emptyBodyPasses bool
	}{
		{newAdmissionCoverageResource("workspaces", "/workspaces", true, RegisterWorkspaceRoutes, workspaceAdmissionResource), true},
		{newAdmissionCoverageResource("roles", "/roles", true, RegisterRoleRoutes, roleAdmissionResource), true},
		{newAdmissionCoverageResource("user profiles", "/user_profiles", true, RegisterUserProfileRoutes, userProfileAdmissionResource), true},
		{newAdmissionCoverageResource("image registries", "/image_registries", true, RegisterImageRegistryRoutes, imageRegistryAdmissionResource), false},
		{newAdmissionCoverageResource("model registries", "/model_registries", true, RegisterModelRegistryRoutes, modelRegistryAdmissionResource), true},
	}

	for _, resource := range legacyResources {
		t.Run(resource.name, func(t *testing.T) {
			var forwardedBodies []string
			upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				body, err := io.ReadAll(request.Body)
				require.NoError(t, err)
				forwardedBodies = append(forwardedBodies, string(body))
				writer.WriteHeader(http.StatusNoContent)
			}))
			t.Cleanup(upstream.Close)

			registry := admission.NewRegistry()
			router := gin.New()
			require.NoError(t, resource.register(router.Group(""), nil, &Dependencies{
				Admission:        registry,
				StorageAccessURL: upstream.URL,
			}))
			require.NoError(t, registry.Seal())

			bodies := []string{`{"future_field":{"enabled":true}}`}
			if resource.emptyBodyPasses {
				bodies = append([]string{""}, bodies...)
			}
			for _, body := range bodies {
				response := newCloseNotifyRecorder()
				request := httptest.NewRequest(http.MethodPost, resource.path, strings.NewReader(body))
				request.Header.Set("Content-Type", "application/json")
				router.ServeHTTP(response, request)
				require.Equal(t, http.StatusNoContent, response.ResponseRecorder.Code)
			}
			require.Len(t, forwardedBodies, len(bodies))
			if resource.emptyBodyPasses {
				require.Empty(t, forwardedBodies[0])
			}
			require.JSONEq(t, `{"future_field":{"enabled":true}}`, forwardedBodies[len(forwardedBodies)-1])
		})
	}
}

func TestLegacyCreateAdmissionRoutesPreserveMalformedBodies(t *testing.T) {
	legacyResources := []admissionCoverageResource{
		newAdmissionCoverageResource("workspaces", "/workspaces", true, RegisterWorkspaceRoutes, workspaceAdmissionResource),
		newAdmissionCoverageResource("roles", "/roles", true, RegisterRoleRoutes, roleAdmissionResource),
		newAdmissionCoverageResource("user profiles", "/user_profiles", true, RegisterUserProfileRoutes, userProfileAdmissionResource),
		newAdmissionCoverageResource("model registries", "/model_registries", true, RegisterModelRegistryRoutes, modelRegistryAdmissionResource),
	}
	const malformedBody = `{"metadata":`

	for _, resource := range legacyResources {
		t.Run(resource.name, func(t *testing.T) {
			var forwardedBody string
			upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				body, err := io.ReadAll(request.Body)
				require.NoError(t, err)
				forwardedBody = string(body)
				writer.WriteHeader(http.StatusNoContent)
			}))
			t.Cleanup(upstream.Close)

			registry := admission.NewRegistry()
			router := gin.New()
			require.NoError(t, resource.register(router.Group(""), nil, &Dependencies{
				Admission:        registry,
				StorageAccessURL: upstream.URL,
			}))
			require.NoError(t, registry.Seal())

			response := newCloseNotifyRecorder()
			request := httptest.NewRequest(http.MethodPost, resource.path, strings.NewReader(malformedBody))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(response, request)

			require.Equal(t, http.StatusNoContent, response.ResponseRecorder.Code)
			require.Equal(t, malformedBody, forwardedBody)
		})
	}
}

func TestImageRegistryURLValidationRemainsOutsideAdmission(t *testing.T) {
	upstreamCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		upstreamCalled = true
	}))
	t.Cleanup(upstream.Close)

	registry := admission.NewRegistry()
	router := gin.New()
	require.NoError(t, RegisterImageRegistryRoutes(router.Group(""), nil, &Dependencies{
		Admission:        registry,
		StorageAccessURL: upstream.URL,
	}))
	admissionHookCalled := false
	require.NoError(t, registry.RegisterHook(imageRegistryAdmissionResource, admission.ValidateCreate(
		admission.HookMeta{Name: "community.coverage.image-url", Order: 900}, 91903,
		func(admission.RequestContext, v1.ImageRegistry) error {
			admissionHookCalled = true
			return nil
		},
	)))
	require.NoError(t, registry.Seal())

	response := newCloseNotifyRecorder()
	request := httptest.NewRequest(http.MethodPost, "/image_registries", strings.NewReader(`{"spec":{"url":"https://index.docker<>.io"}}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusBadRequest, response.ResponseRecorder.Code)
	require.Contains(t, response.ResponseRecorder.Body.String(), "invalid image registry url")
	require.False(t, upstreamCalled)
	require.False(t, admissionHookCalled)
}

func defaultRESTAdmissionCoverageResources() []admissionCoverageResource {
	return []admissionCoverageResource{
		newAdmissionCoverageResource("api keys", "/api_keys", false, RegisterAPIKeyRoutes, apiKeyAdmissionResource),
		newAdmissionCoverageResource("workspaces", "/workspaces", true, RegisterWorkspaceRoutes, workspaceAdmissionResource),
		newAdmissionCoverageResource("roles", "/roles", true, RegisterRoleRoutes, roleAdmissionResource),
		newAdmissionCoverageResource("role assignments", "/role_assignments", true, RegisterRoleAssignmentRoutes, roleAssignmentAdmissionResource),
		newAdmissionCoverageResource("user profiles", "/user_profiles", true, RegisterUserProfileRoutes, userProfileAdmissionResource),
		newAdmissionCoverageResource("clusters", "/clusters", true, RegisterClusterRoutes, clusterAdmissionResource),
		newAdmissionCoverageResource("image registries", "/image_registries", true, RegisterImageRegistryRoutes, imageRegistryAdmissionResource),
		newAdmissionCoverageResource("model registries", "/model_registries", true, RegisterModelRegistryRoutes, modelRegistryAdmissionResource),
		newAdmissionCoverageResource("endpoints", "/endpoints", true, RegisterEndpointRoutes, endpointAdmissionResource),
		newAdmissionCoverageResource("engines", "/engines", true, RegisterEngineRoutes, engineAdmissionResource),
		newAdmissionCoverageResource("model catalogs", "/model_catalogs", true, RegisterModelCatalogRoutes, modelCatalogAdmissionResource),
		newAdmissionCoverageResource("oem configs", "/oem_configs", true, RegisterOEMConfigRoutes, oemConfigAdmissionResource),
		newAdmissionCoverageResource("external endpoints", "/external_endpoints", true, RegisterExternalEndpointRoutes, externalEndpointAdmissionResource),
	}
}

func newAdmissionCoverageResource[T any](name, path string, hasCreate bool, register func(*gin.RouterGroup, []gin.HandlerFunc, *Dependencies) error, resource admission.Resource[T]) admissionCoverageResource {
	return admissionCoverageResource{
		name:      name,
		path:      path,
		hasCreate: hasCreate,
		register:  register,
		assertTyped: func(registry *admission.Registry) error {
			return registry.RegisterResource(resource)
		},
		registerHooks: func(registry *admission.Registry) error {
			if hasCreate {
				if err := registry.RegisterHook(resource, admission.ValidateCreate(
					admission.HookMeta{Name: "community.coverage.create", Order: 900}, coverageCreateCode,
					func(admission.RequestContext, T) error {
						return &admission.Error{Code: coverageCreateCode, Message: "create admission runner invoked"}
					},
				)); err != nil {
					return err
				}
			}
			return registry.RegisterHook(resource, admission.ValidateUpdate(
				admission.HookMeta{Name: "community.coverage.update", Order: 901}, coverageUpdateCode,
				func(admission.RequestContext, T, T) error {
					return &admission.Error{Code: coverageUpdateCode, Message: "patch admission runner invoked"}
				},
			))
		},
	}
}
