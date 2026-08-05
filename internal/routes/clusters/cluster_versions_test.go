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

func TestGetAvailableClusterVersionsReturnsCompatibleStoredProfiles(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := storageMocks.NewMockStorage(t)
	store.On("ListClusterProfile", mock.Anything).Return([]v1.ClusterProfile{
		clusterProfile("v1.2.0"),
		clusterProfile("v1.1.0-rc.1"),
		clusterProfile("v1.2.0-alpha.1"),
		clusterProfile("v1.2.0-rc.1"),
		clusterProfile("v1.3.0"),
		clusterProfile("not-a-version"),
	}, nil).Once()

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/clusters/available_versions", nil)

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

func clusterProfile(version string) v1.ClusterProfile {
	return v1.ClusterProfile{Metadata: &v1.Metadata{Name: version}}
}
