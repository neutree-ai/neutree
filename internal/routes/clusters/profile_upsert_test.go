package clusters

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	storageMocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func TestProfileUpsertCreatesCompleteExactProfile(t *testing.T) {
	store := storageMocks.NewMockStorage(t)
	allowClusterProfileUpsert(store)
	store.On("ListClusterProfile", mock.Anything).Return([]v1.ClusterProfile{}, nil).Once()
	store.On("CreateClusterProfile", mock.MatchedBy(func(profile *v1.ClusterProfile) bool {
		return profile.GetName() == "v1.2.0-alpha.1" && len(profile.Spec.Components) == 2
	})).Return(nil).Once()

	response := executeProfileUpsert(t, store, validProfile("v1.2.0-alpha.1"))
	assert.Equal(t, http.StatusOK, response.Code)
	assert.Contains(t, response.Body.String(), `"created"`)
}

func TestProfileUpsertReplaysIdenticalProfileAndRejectsDrift(t *testing.T) {
	profile := validProfile("v1.2.0-alpha.1")

	t.Run("unchanged", func(t *testing.T) {
		store := storageMocks.NewMockStorage(t)
		allowClusterProfileUpsert(store)
		store.On("ListClusterProfile", mock.Anything).Return([]v1.ClusterProfile{*profile}, nil).Once()

		response := executeProfileUpsert(t, store, profile)
		assert.Equal(t, http.StatusOK, response.Code)
		assert.Contains(t, response.Body.String(), `"unchanged"`)
	})

	t.Run("drift", func(t *testing.T) {
		stored := validProfile("v1.2.0-alpha.1")
		request := validProfile("v1.2.0-alpha.1")
		ssh, found := request.Spec.ComponentsFor(v1.SSHClusterType)
		require.True(t, found)
		ssh.RayRuntime.Tag = "v1.2.0-alpha.2"
		request.Spec.Components[v1.SSHClusterType] = ssh

		store := storageMocks.NewMockStorage(t)
		allowClusterProfileUpsert(store)
		store.On("ListClusterProfile", mock.Anything).Return([]v1.ClusterProfile{*stored}, nil).Once()

		response := executeProfileUpsert(t, store, request)
		assert.Equal(t, http.StatusConflict, response.Code)
	})

	t.Run("server timestamps do not create drift", func(t *testing.T) {
		stored := validProfile("v1.2.0-alpha.1")
		stored.Metadata.CreationTimestamp = "2026-01-01T00:00:00Z"
		stored.Metadata.UpdateTimestamp = "2026-01-02T00:00:00Z"
		stored.Metadata.DeletionTimestamp = "2026-01-03T00:00:00Z"

		store := storageMocks.NewMockStorage(t)
		allowClusterProfileUpsert(store)
		store.On("ListClusterProfile", mock.Anything).Return([]v1.ClusterProfile{*stored}, nil).Once()

		response := executeProfileUpsert(t, store, validProfile("v1.2.0-alpha.1"))
		assert.Equal(t, http.StatusOK, response.Code)
		assert.Contains(t, response.Body.String(), `"unchanged"`)
	})

	t.Run("empty metadata maps do not create drift", func(t *testing.T) {
		stored := validProfile("v1.2.0-alpha.1")
		stored.Metadata.Labels = map[string]string{}
		stored.Metadata.Annotations = map[string]string{}

		store := storageMocks.NewMockStorage(t)
		allowClusterProfileUpsert(store)
		store.On("ListClusterProfile", mock.Anything).Return([]v1.ClusterProfile{*stored}, nil).Once()

		response := executeProfileUpsert(t, store, validProfile("v1.2.0-alpha.1"))
		assert.Equal(t, http.StatusOK, response.Code)
		assert.Contains(t, response.Body.String(), `"unchanged"`)
	})
}

func TestProfileUpsertRejectsForceUpdateFieldIncludingNull(t *testing.T) {
	for _, value := range []any{true, false, nil} {
		t.Run(jsonValueName(value), func(t *testing.T) {
			store := storageMocks.NewMockStorage(t)
			allowClusterProfileUpsert(store)
			body := map[string]any{"profile": validProfile("v1.2.0-alpha.1"), "force_update": value}

			response := executeProfileUpsertBody(t, store, body)
			assert.Equal(t, http.StatusBadRequest, response.Code)
			assert.Contains(t, response.Body.String(), "force_update")
		})
	}
}

func TestProfileUpsertRejectsVersionAboveDefaultOrIncompatibleMinor(t *testing.T) {
	for _, version := range []string{"v1.2.1", "v1.3.0"} {
		t.Run(version, func(t *testing.T) {
			store := storageMocks.NewMockStorage(t)
			allowClusterProfileUpsert(store)
			response := executeProfileUpsert(t, store, validProfile(version))
			assert.Equal(t, http.StatusBadRequest, response.Code)
		})
	}
}

func TestProfileUpsertRequiresBothMatricesAndExactSemver(t *testing.T) {
	cases := []func(*v1.ClusterProfile){
		func(profile *v1.ClusterProfile) { delete(profile.Spec.Components, v1.KubernetesClusterType) },
		func(profile *v1.ClusterProfile) { profile.Metadata.Name = "v1.2" },
	}
	for index, mutate := range cases {
		t.Run(string(rune('a'+index)), func(t *testing.T) {
			profile := validProfile("v1.2.0-alpha.1")
			mutate(profile)
			store := storageMocks.NewMockStorage(t)
			allowClusterProfileUpsert(store)
			response := executeProfileUpsert(t, store, profile)
			assert.Equal(t, http.StatusBadRequest, response.Code)
		})
	}
}

func TestProfileUpsertRequiresSystemAdminPermission(t *testing.T) {
	store := storageMocks.NewMockStorage(t)
	store.On("CallDatabaseFunction", "has_permission", mock.MatchedBy(func(params map[string]interface{}) bool {
		return params["user_uuid"] == "unprivileged-user" && params["required_permission"] == "system:admin" && params["workspace"] == nil
	}), mock.Anything).Run(func(args mock.Arguments) {
		result := args.Get(2).(*bool)
		*result = false
	}).Return(nil).Once()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(context *gin.Context) { context.Set("user_id", "unprivileged-user") })
	RegisterClusterRoutes(router.Group("/api/v1"), nil, &Dependencies{Storage: store, ReleaseInfoProvider: &testReleaseInfoProvider{info: semanticReleaseInfo()}})

	body, err := json.Marshal(map[string]any{"profile": validProfile("v1.2.0-alpha.1")})
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/clusters/profile_upsert", bytes.NewReader(body)))
	assert.Equal(t, http.StatusForbidden, recorder.Code)
}

func executeProfileUpsert(t *testing.T, store *storageMocks.MockStorage, profile *v1.ClusterProfile) *httptest.ResponseRecorder {
	return executeProfileUpsertBody(t, store, map[string]any{"profile": profile})
}

func executeProfileUpsertBody(t *testing.T, store *storageMocks.MockStorage, payload map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(context *gin.Context) { context.Set("user_id", "administrator") })
	RegisterClusterRoutes(router.Group("/api/v1"), nil, &Dependencies{Storage: store, ReleaseInfoProvider: &testReleaseInfoProvider{info: semanticReleaseInfo()}})
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/clusters/profile_upsert", bytes.NewReader(body)))
	return recorder
}

func allowClusterProfileUpsert(store *storageMocks.MockStorage) {
	store.On("CallDatabaseFunction", "has_permission", mock.MatchedBy(func(params map[string]interface{}) bool {
		return params["user_uuid"] == "administrator" && params["required_permission"] == "system:admin" && params["workspace"] == nil
	}), mock.Anything).Run(func(args mock.Arguments) {
		result := args.Get(2).(*bool)
		*result = true
	}).Return(nil).Once()
}

func semanticReleaseInfo() *v1.ReleaseInfo {
	return &v1.ReleaseInfo{Metadata: &v1.Metadata{Name: "v1.2.0"}, Spec: &v1.ReleaseInfoSpec{
		DefaultClusterVersion: "v1.2.0", CompatibleClusterBaselines: []string{"v1.1", "v1.2"},
	}}
}

func validProfile(version string) *v1.ClusterProfile {
	return &v1.ClusterProfile{APIVersion: "v1", Kind: v1.ClusterProfileKind, Metadata: &v1.Metadata{Name: version}, Spec: &v1.ClusterProfileSpec{Components: map[string]v1.ClusterProfileComponents{
		v1.SSHClusterType:        {RayRuntime: v1.ImageRef{Image: "neutree/neutree-serve", Tag: version}, NodeAgent: v1.ImageRef{Image: "neutree/node-agent", Tag: version}, NodeExporter: v1.ImageRef{Image: "prom/node-exporter", Tag: "v1.8.2"}, VMAgent: v1.ImageRef{Image: "vmagent", Tag: "v1"}},
		v1.KubernetesClusterType: {KubernetesRuntime: v1.ImageRef{Image: "neutree/runtime", Tag: version}, Router: v1.ImageRef{Image: "neutree/router", Tag: version}, NodeAgent: v1.ImageRef{Image: "neutree/node-agent", Tag: version}, NodeExporter: v1.ImageRef{Image: "prom/node-exporter", Tag: "v1.8.2"}, VMAgent: v1.ImageRef{Image: "vmagent", Tag: "v1"}, KubeStateMetrics: v1.ImageRef{Image: "kube-state", Tag: "v2"}},
	}}}
}

func jsonValueName(value any) string {
	if value == nil {
		return "null"
	}
	return fmt.Sprint(value)
}
