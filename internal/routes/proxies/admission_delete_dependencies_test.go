package proxies

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/admission"
	"github.com/neutree-ai/neutree/pkg/storage"
	storageMocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func TestDeleteDependencyAdmissionHooksPreserveRejections(t *testing.T) {
	t.Run("workspace", func(t *testing.T) {
		store := storageMocks.NewMockStorage(t)
		workspaceDeleteCounts(store, map[string]int{storage.ENDPOINT_TABLE: 2})
		registry := admission.NewRegistry()
		require.NoError(t, RegisterWorkspaceRoutes(gin.New().Group("/resource"), nil, &Dependencies{Admission: registry, Storage: store}))
		require.NoError(t, registry.Seal())

		chain, err := registry.Chain(workspaceAdmissionResource, admission.Delete)
		require.NoError(t, err)
		require.Equal(t, "community.workspace.dependencies.delete", chain.Hooks()[0].Name)
		_, err = chain.Run(admission.RequestContext{Context: context.Background()}, v1.Workspace{}, deleteWorkspace("default"))
		requireDeleteAdmissionError(t, err, 10125, "cannot delete workspace 'default'", "Resources still exist in this workspace:\n- endpoints: 2")
	})

	t.Run("cluster", func(t *testing.T) {
		store := storageMocks.NewMockStorage(t)
		store.EXPECT().Count(storage.ENDPOINT_TABLE, clusterEndpointReferenceFilters("default", "cluster")).Return(5, nil)
		registry := admission.NewRegistry()
		require.NoError(t, RegisterClusterRoutes(gin.New().Group("/resource"), nil, &Dependencies{Admission: registry, Storage: store}))
		require.NoError(t, registry.Seal())

		chain, err := registry.Chain(clusterAdmissionResource, admission.Delete)
		require.NoError(t, err)
		require.Equal(t, "community.cluster.dependencies.delete", chain.Hooks()[0].Name)
		_, err = chain.Run(admission.RequestContext{Context: context.Background()}, v1.Cluster{}, deleteCluster("default", "cluster"))
		requireDeleteAdmissionError(t, err, 10126, "cannot delete cluster 'default/cluster'", "5 endpoint(s) still reference this cluster")
	})

	t.Run("image registry", func(t *testing.T) {
		store := storageMocks.NewMockStorage(t)
		store.EXPECT().Count(storage.CLUSTERS_TABLE, []storage.Filter{
			{Column: "metadata->>workspace", Operator: "eq", Value: "default"},
			{Column: "spec->>image_registry", Operator: "eq", Value: "registry"},
		}).Return(3, nil)
		registry := admission.NewRegistry()
		require.NoError(t, RegisterImageRegistryRoutes(gin.New().Group("/resource"), nil, &Dependencies{Admission: registry, Storage: store}))
		require.NoError(t, registry.Seal())

		chain, err := registry.Chain(imageRegistryAdmissionResource, admission.Delete)
		require.NoError(t, err)
		require.Equal(t, "community.image-registry.dependencies.delete", chain.Hooks()[0].Name)
		_, err = chain.Run(admission.RequestContext{Context: context.Background()}, v1.ImageRegistry{}, deleteImageRegistry("default", "registry"))
		requireDeleteAdmissionError(t, err, 10127, "cannot delete image_registry 'default/registry'", "3 cluster(s) still reference this image registry")
	})

	t.Run("model registry", func(t *testing.T) {
		store := storageMocks.NewMockStorage(t)
		store.EXPECT().Count(storage.ENDPOINT_TABLE, []storage.Filter{
			{Column: "metadata->>workspace", Operator: "eq", Value: "default"},
			{Column: "spec->model->>registry", Operator: "eq", Value: "registry"},
		}).Return(2, nil)
		registry := admission.NewRegistry()
		require.NoError(t, RegisterModelRegistryRoutes(gin.New().Group("/resource"), nil, &Dependencies{Admission: registry, Storage: store}))
		require.NoError(t, registry.Seal())

		chain, err := registry.Chain(modelRegistryAdmissionResource, admission.Delete)
		require.NoError(t, err)
		require.Equal(t, "community.model-registry.dependencies.delete", chain.Hooks()[0].Name)
		_, err = chain.Run(admission.RequestContext{Context: context.Background()}, v1.ModelRegistry{}, deleteModelRegistry("default", "registry"))
		requireDeleteAdmissionError(t, err, 10128, "cannot delete model_registry 'default/registry'", "2 endpoint(s) still reference this model registry")
	})

	t.Run("global role", func(t *testing.T) {
		store := storageMocks.NewMockStorage(t)
		store.EXPECT().Count(storage.ROLE_ASSIGNMENT_TABLE, []storage.Filter{
			{Column: "spec->>role", Operator: "eq", Value: "admin"},
			{Column: "metadata->>workspace", Operator: "is", Value: "null"},
		}).Return(1, nil)
		registry := admission.NewRegistry()
		require.NoError(t, RegisterRoleRoutes(gin.New().Group("/resource"), nil, &Dependencies{Admission: registry, Storage: store}))
		require.NoError(t, registry.Seal())

		chain, err := registry.Chain(roleAdmissionResource, admission.Delete)
		require.NoError(t, err)
		require.Equal(t, "community.role.dependencies.delete", chain.Hooks()[0].Name)
		_, err = chain.Run(admission.RequestContext{Context: context.Background()}, v1.Role{}, deleteRole("", "admin"))
		requireDeleteAdmissionError(t, err, 10129, "cannot delete role 'global/admin'", "1 role assignment(s) still reference this role")
	})

	t.Run("user profile", func(t *testing.T) {
		store := storageMocks.NewMockStorage(t)
		store.EXPECT().ListUserProfile(storage.ListOption{Filters: []storage.Filter{
			{Column: "metadata->>name", Operator: "eq", Value: "alice"},
		}}).Return([]v1.UserProfile{{ID: "user-id"}}, nil)
		store.EXPECT().Count(storage.ROLE_ASSIGNMENT_TABLE, []storage.Filter{
			{Column: "spec->>user_id", Operator: "eq", Value: "user-id"},
		}).Return(4, nil)
		registry := admission.NewRegistry()
		require.NoError(t, RegisterUserProfileRoutes(gin.New().Group("/resource"), nil, &Dependencies{Admission: registry, Storage: store}))
		require.NoError(t, registry.Seal())

		chain, err := registry.Chain(userProfileAdmissionResource, admission.Delete)
		require.NoError(t, err)
		require.Equal(t, "community.user-profile.dependencies.delete", chain.Hooks()[0].Name)
		_, err = chain.Run(admission.RequestContext{Context: context.Background()}, v1.UserProfile{}, deleteUserProfile("alice"))
		requireDeleteAdmissionError(t, err, 10130, "cannot delete user_profile 'alice'", "4 role assignment(s) still reference this user")
	})
}

func TestDeleteDependencyAdmissionRunnerPreservesLegacyResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	testCases := []struct {
		name    string
		code    string
		message string
		hint    string
		path    string
		body    string
		runner  func(*testing.T) gin.HandlerFunc
	}{
		{
			name:    "workspace",
			code:    "10125",
			message: "cannot delete workspace 'default'",
			hint:    "Resources still exist in this workspace:\n- endpoints: 2",
			path:    "/?metadata-%3E%3Ename=eq.default&metadata-%3E%3Eworkspace=eq.default",
			body:    deleteRequestBody("default", "default"),
			runner: func(t *testing.T) gin.HandlerFunc {
				store := storageMocks.NewMockStorage(t)
				workspaceDeleteCounts(store, map[string]int{storage.ENDPOINT_TABLE: 2})
				registry := admission.NewRegistry()
				require.NoError(t, RegisterWorkspaceRoutes(gin.New().Group("/resource"), nil, &Dependencies{Admission: registry, Storage: store}))
				require.NoError(t, registry.Seal())
				return deleteDependencyAdmissionRunner(t, registry, workspaceAdmissionResource, storage.WORKSPACE_TABLE, `{"metadata":{"name":"default","workspace":"default"}}`)
			},
		},
		{
			name:    "cluster",
			code:    "10126",
			message: "cannot delete cluster 'default/cluster'",
			hint:    "5 endpoint(s) still reference this cluster",
			path:    "/?metadata-%3E%3Ename=eq.cluster&metadata-%3E%3Eworkspace=eq.default",
			body:    deleteRequestBody("default", "cluster"),
			runner: func(t *testing.T) gin.HandlerFunc {
				store := storageMocks.NewMockStorage(t)
				store.EXPECT().Count(storage.ENDPOINT_TABLE, clusterEndpointReferenceFilters("default", "cluster")).Return(5, nil)
				registry := admission.NewRegistry()
				require.NoError(t, RegisterClusterRoutes(gin.New().Group("/resource"), nil, &Dependencies{Admission: registry, Storage: store}))
				require.NoError(t, registry.Seal())
				return deleteDependencyAdmissionRunner(t, registry, clusterAdmissionResource, storage.CLUSTERS_TABLE, `{"metadata":{"name":"cluster","workspace":"default"}}`)
			},
		},
		{
			name:    "image registry",
			code:    "10127",
			message: "cannot delete image_registry 'default/registry'",
			hint:    "3 cluster(s) still reference this image registry",
			path:    "/?metadata-%3E%3Ename=eq.registry&metadata-%3E%3Eworkspace=eq.default",
			body:    deleteRequestBody("default", "registry"),
			runner: func(t *testing.T) gin.HandlerFunc {
				store := storageMocks.NewMockStorage(t)
				store.EXPECT().Count(storage.CLUSTERS_TABLE, []storage.Filter{
					{Column: "metadata->>workspace", Operator: "eq", Value: "default"},
					{Column: "spec->>image_registry", Operator: "eq", Value: "registry"},
				}).Return(3, nil)
				registry := admission.NewRegistry()
				require.NoError(t, RegisterImageRegistryRoutes(gin.New().Group("/resource"), nil, &Dependencies{Admission: registry, Storage: store}))
				require.NoError(t, registry.Seal())
				return deleteDependencyAdmissionRunner(
					t,
					registry,
					imageRegistryAdmissionResource,
					storage.IMAGE_REGISTRY_TABLE,
					`{"metadata":{"name":"registry","workspace":"default"}}`,
				)
			},
		},
		{
			name:    "model registry",
			code:    "10128",
			message: "cannot delete model_registry 'default/registry'",
			hint:    "2 endpoint(s) still reference this model registry",
			path:    "/?metadata-%3E%3Ename=eq.registry&metadata-%3E%3Eworkspace=eq.default",
			body:    deleteRequestBody("default", "registry"),
			runner: func(t *testing.T) gin.HandlerFunc {
				store := storageMocks.NewMockStorage(t)
				store.EXPECT().Count(storage.ENDPOINT_TABLE, []storage.Filter{
					{Column: "metadata->>workspace", Operator: "eq", Value: "default"},
					{Column: "spec->model->>registry", Operator: "eq", Value: "registry"},
				}).Return(2, nil)
				registry := admission.NewRegistry()
				require.NoError(t, RegisterModelRegistryRoutes(gin.New().Group("/resource"), nil, &Dependencies{Admission: registry, Storage: store}))
				require.NoError(t, registry.Seal())
				return deleteDependencyAdmissionRunner(
					t,
					registry,
					modelRegistryAdmissionResource,
					storage.MODEL_REGISTRY_TABLE,
					`{"metadata":{"name":"registry","workspace":"default"}}`,
				)
			},
		},
		{
			name:    "global role",
			code:    "10129",
			message: "cannot delete role 'global/admin'",
			hint:    "1 role assignment(s) still reference this role",
			path:    "/?metadata-%3E%3Ename=eq.admin&metadata-%3E%3Eworkspace=is.null",
			body:    deleteRequestBody("", "admin"),
			runner: func(t *testing.T) gin.HandlerFunc {
				store := storageMocks.NewMockStorage(t)
				store.EXPECT().Count(storage.ROLE_ASSIGNMENT_TABLE, []storage.Filter{
					{Column: "spec->>role", Operator: "eq", Value: "admin"},
					{Column: "metadata->>workspace", Operator: "is", Value: "null"},
				}).Return(1, nil)
				registry := admission.NewRegistry()
				require.NoError(t, RegisterRoleRoutes(gin.New().Group("/resource"), nil, &Dependencies{Admission: registry, Storage: store}))
				require.NoError(t, registry.Seal())
				return deleteDependencyAdmissionRunner(t, registry, roleAdmissionResource, storage.ROLE_TABLE, `{"metadata":{"name":"admin"}}`)
			},
		},
		{
			name:    "user profile",
			code:    "10130",
			message: "cannot delete user_profile 'alice'",
			hint:    "4 role assignment(s) still reference this user",
			path:    "/?metadata-%3E%3Ename=eq.alice&metadata-%3E%3Eworkspace=eq.default",
			body:    deleteRequestBody("default", "alice"),
			runner: func(t *testing.T) gin.HandlerFunc {
				store := storageMocks.NewMockStorage(t)
				store.EXPECT().ListUserProfile(storage.ListOption{Filters: []storage.Filter{
					{Column: "metadata->>name", Operator: "eq", Value: "alice"},
				}}).Return([]v1.UserProfile{{ID: "user-id"}}, nil)
				store.EXPECT().Count(storage.ROLE_ASSIGNMENT_TABLE, []storage.Filter{
					{Column: "spec->>user_id", Operator: "eq", Value: "user-id"},
				}).Return(4, nil)
				registry := admission.NewRegistry()
				require.NoError(t, RegisterUserProfileRoutes(gin.New().Group("/resource"), nil, &Dependencies{Admission: registry, Storage: store}))
				require.NoError(t, registry.Seal())
				return deleteDependencyAdmissionRunner(t, registry, userProfileAdmissionResource, storage.USER_PROFILE_TABLE, `{"metadata":{"name":"alice","workspace":"default"}}`)
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			forwarded := false
			router := gin.New()
			router.PATCH("/", func(c *gin.Context) { c.Set("postgrest_token", "postgrest-jwt") }, testCase.runner(t), func(*gin.Context) { forwarded = true })
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPatch, testCase.path, bytes.NewBufferString(testCase.body))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, request)

			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Equal(t, "Neutree", recorder.Header().Get("X-Powered-By"))
			require.JSONEq(t, fmt.Sprintf(`{"code":"%s","message":%q,"hint":%q}`, testCase.code, testCase.message, testCase.hint), recorder.Body.String())
			require.False(t, forwarded)
		})
	}
}

func deleteDependencyAdmissionRunner[T any](t *testing.T, registry *admission.Registry, resource admission.Resource[T], table, old string) gin.HandlerFunc {
	t.Helper()
	return newPatchAdmissionRunner(
		registryPatchAdmissionChainResolver{registry: registry},
		&fakePatchAdmissionReader{targets: []json.RawMessage{json.RawMessage(old)}},
		resource,
		table,
	)
}

func deleteRequestBody(workspace, name string) string {
	metadata := fmt.Sprintf(`"name":%q,"deletion_timestamp":"2026-08-05T00:00:00Z","annotations":{"neutree.ai/force-delete":"true"}`, name)
	if workspace != "" {
		metadata = fmt.Sprintf(`"workspace":%q,%s`, workspace, metadata)
	}
	return fmt.Sprintf(`{"metadata":{%s}}`, metadata)
}

func workspaceDeleteCounts(store *storageMocks.MockStorage, counts map[string]int) {
	for _, table := range []string{
		storage.ENDPOINT_TABLE,
		storage.CLUSTERS_TABLE,
		storage.MODEL_REGISTRY_TABLE,
		storage.IMAGE_REGISTRY_TABLE,
		storage.MODEL_CATALOG_TABLE,
		storage.ROLE_TABLE,
		storage.API_KEY_TABLE,
	} {
		store.EXPECT().Count(table, []storage.Filter{
			{Column: "metadata->>workspace", Operator: "eq", Value: "default"},
		}).Return(counts[table], nil)
	}
	store.EXPECT().Count(storage.ROLE_ASSIGNMENT_TABLE, []storage.Filter{
		{Column: "spec->>workspace", Operator: "eq", Value: "default"},
	}).Return(counts[storage.ROLE_ASSIGNMENT_TABLE], nil)
}

func deleteWorkspace(name string) v1.Workspace {
	return v1.Workspace{Metadata: deleteMetadata("", name)}
}

func deleteCluster(workspace, name string) v1.Cluster {
	return v1.Cluster{Metadata: deleteMetadata(workspace, name)}
}

func deleteImageRegistry(workspace, name string) v1.ImageRegistry {
	return v1.ImageRegistry{Metadata: deleteMetadata(workspace, name)}
}

func deleteModelRegistry(workspace, name string) v1.ModelRegistry {
	return v1.ModelRegistry{Metadata: deleteMetadata(workspace, name)}
}

func deleteRole(workspace, name string) v1.Role {
	return v1.Role{Metadata: deleteMetadata(workspace, name)}
}

func deleteUserProfile(name string) v1.UserProfile {
	return v1.UserProfile{Metadata: deleteMetadata("", name)}
}

func deleteMetadata(workspace, name string) *v1.Metadata {
	return &v1.Metadata{
		Workspace:         workspace,
		Name:              name,
		DeletionTimestamp: "2026-08-05T00:00:00Z",
		Annotations:       map[string]string{"neutree.ai/force-delete": "true"},
	}
}

func requireDeleteAdmissionError(t *testing.T, err error, code int, message, hint string) {
	t.Helper()
	var admissionErr *admission.Error
	require.ErrorAs(t, err, &admissionErr)
	require.Equal(t, code, admissionErr.Code)
	require.Equal(t, message, admissionErr.Message)
	require.Equal(t, hint, admissionErr.Hint)
}
