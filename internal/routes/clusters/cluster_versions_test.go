package clusters

import (
	"encoding/json"
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

func TestGetAvailableClusterVersionsReturnsCompatibleStoredProfilesForRequestedType(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := storageMocks.NewMockStorage(t)
	store.On("ListClusterProfile", mock.Anything).Return([]v1.ClusterProfile{
		clusterProfile("v1.2.0", v1.SSHClusterType),
		clusterProfile("v1.2.0", v1.KubernetesClusterType),
		clusterProfile("v1.1.0-rc.1", v1.SSHClusterType),
		clusterProfile("v1.2.0-alpha.1", v1.SSHClusterType),
		clusterProfile("v1.2.0-rc.1", v1.SSHClusterType),
		clusterProfile("v1.2.0-rc.1", v1.KubernetesClusterType),
		clusterProfile("v1.3.0", v1.SSHClusterType),
		clusterProfile("not-a-version", v1.SSHClusterType),
	}, nil).Once()

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/clusters/available_versions?cluster_type=ssh", nil)

	getAvailableClusterVersions(&Dependencies{
		Storage:             store,
		ReleaseInfoProvider: &testReleaseInfoProvider{info: semanticReleaseInfo()},
	})(context)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response availableClusterVersionsResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, []string{"v1.2.0-alpha.1", "v1.2.0-rc.1", "v1.2.0"}, response.AvailableVersions)
	store.AssertExpectations(t)
}

func TestGetAvailableClusterVersionsRequiresValidClusterType(t *testing.T) {
	for _, target := range []string{"", "?cluster_type=docker", "?cluster_type=%20ssh%20"} {
		t.Run(target, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodGet, "/clusters/available_versions"+target, nil)

			getAvailableClusterVersions(&Dependencies{})(context)

			assert.Equal(t, http.StatusBadRequest, recorder.Code)
		})
	}
}

func TestGetClusterProfileVersionsReturnsOnlyValidSortedIdentities(t *testing.T) {
	store := storageMocks.NewMockStorage(t)
	store.On("ListClusterProfile", mock.Anything).Return([]v1.ClusterProfile{
		clusterProfile("v1.2.0", v1.SSHClusterType),
		clusterProfile("v1.1.1", v1.KubernetesClusterType),
		clusterProfile("v1.2.0", v1.KubernetesClusterType),
		clusterProfile("", v1.SSHClusterType),
		clusterProfile("v1.2.0", "unknown"),
		clusterProfile("v1.2.0", " ssh "),
	}, nil).Once()
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)

	getClusterProfileVersions(&Dependencies{Storage: store})(context)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response clusterProfileVersionsResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, []clusterProfileVersion{
		{Version: "v1.1.1", ClusterType: v1.KubernetesClusterType},
		{Version: "v1.2.0", ClusterType: v1.KubernetesClusterType},
		{Version: "v1.2.0", ClusterType: v1.SSHClusterType},
	}, response.Profiles)
	store.AssertExpectations(t)
}

func TestGetClusterProfileVersionsRequiresSystemAdminPermission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := storageMocks.NewMockStorage(t)
	store.On("CallDatabaseFunction", "has_permission", mock.MatchedBy(func(params map[string]interface{}) bool {
		return params["user_uuid"] == "unprivileged-user" &&
			params["required_permission"] == "system:admin" &&
			params["workspace"] == nil
	}), mock.Anything).Run(func(args mock.Arguments) {
		result := args.Get(2).(*bool)
		*result = false
	}).Return(nil).Once()

	router := gin.New()
	router.Use(func(context *gin.Context) {
		context.Set("user_id", "unprivileged-user")
	})
	RegisterClusterRoutes(router.Group("/api/v1"), nil, &Dependencies{Storage: store})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/clusters/profile_versions", nil))

	require.Equal(t, http.StatusForbidden, recorder.Code)
}

type testReleaseInfoProvider struct {
	info *v1.ReleaseInfo
	err  error
}

func (provider *testReleaseInfoProvider) Current() (*v1.ReleaseInfo, error) {
	return provider.info, provider.err
}

func semanticReleaseInfo() *v1.ReleaseInfo {
	return &v1.ReleaseInfo{
		Metadata: &v1.Metadata{Name: "v1.2.0"},
		Spec: &v1.ReleaseInfoSpec{
			CompatibleClusterBaselines: []string{"v1.1", "v1.2"},
		},
	}
}

func clusterProfile(version, clusterType string) v1.ClusterProfile {
	return v1.ClusterProfile{Metadata: &v1.Metadata{Name: version}, Spec: &v1.ClusterProfileSpec{ClusterType: clusterType}}
}
