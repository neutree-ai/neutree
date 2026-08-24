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
	registryMocks "github.com/neutree-ai/neutree/internal/registry/mocks"
	storageMocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func TestGetAvailableClusterVersionsChecksTargetRegistryAndReturnsDefault(t *testing.T) {
	store := storageMocks.NewMockStorage(t)
	store.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{testImageRegistry()}, nil).Once()
	store.On("ListClusterProfile", mock.Anything).Return([]v1.ClusterProfile{
		*completeClusterProfile("v1.1.0"),
		*completeClusterProfile("v1.2.0"),
	}, nil).Once()

	images := registryMocks.NewMockImageService(t)
	images.On("ListImageTags", "registry.example.com/neutree/neutree/neutree-serve", mock.Anything, false).Return([]string{"v1.1.1", "v1.2.0"}, nil).Once()
	images.On("GetImageLabels", mock.Anything, mock.Anything, false).Return(map[string]string{
		v1.NeutreeServingVersionLabel: "v1.1.0",
		"neutree.ai/accelerator-type": "cuda",
	}, nil).Once()
	images.On("GetImageLabels", mock.Anything, mock.Anything, false).Return(map[string]string{
		v1.NeutreeServingVersionLabel: "v1.2.0",
		"neutree.ai/accelerator-type": "cuda",
	}, nil).Once()
	images.On("CheckImageExists", mock.Anything, mock.Anything, false).Return(true, nil).Times(6)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/clusters/available_versions?workspace=default&image_registry=registry&cluster_type=ssh", nil)

	getAvailableClusterVersions(&Dependencies{
		Storage:             store,
		ReleaseInfoProvider: &testReleaseInfoProvider{info: semanticReleaseInfo()},
		ImageService:        images,
	})(context)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response availableClusterVersionsResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, []string{"v1.1.0", "v1.2.0"}, response.AvailableVersions)
	require.NotNil(t, response.DefaultClusterVersion)
	assert.Equal(t, "v1.2.0", *response.DefaultClusterVersion)
}

func TestGetAvailableClusterVersionsReturnsNullDefaultWhenDefaultImagesMissing(t *testing.T) {
	store := storageMocks.NewMockStorage(t)
	store.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{testImageRegistry()}, nil).Once()
	store.On("ListClusterProfile", mock.Anything).Return([]v1.ClusterProfile{*completeClusterProfile("v1.2.0")}, nil).Once()
	images := registryMocks.NewMockImageService(t)
	images.On("ListImageTags", mock.Anything, mock.Anything, false).Return([]string{}, nil).Once()

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/clusters/available_versions?workspace=default&image_registry=registry&cluster_type=ssh", nil)

	getAvailableClusterVersions(&Dependencies{Storage: store, ReleaseInfoProvider: &testReleaseInfoProvider{info: semanticReleaseInfo()}, ImageService: images})(context)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response availableClusterVersionsResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Empty(t, response.AvailableVersions)
	assert.Nil(t, response.DefaultClusterVersion)
}

func TestGetAvailableClusterVersionsRequiresExactDefaultProfileIdentity(t *testing.T) {
	store := storageMocks.NewMockStorage(t)
	store.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{testImageRegistry()}, nil).Once()
	store.On("ListClusterProfile", mock.Anything).Return([]v1.ClusterProfile{*completeClusterProfile("v1.2.0+build.1")}, nil).Once()

	images := registryMocks.NewMockImageService(t)
	images.On("ListImageTags", mock.Anything, mock.Anything, false).Return([]string{"build"}, nil).Once()
	images.On("GetImageLabels", mock.Anything, mock.Anything, false).Return(map[string]string{
		v1.NeutreeServingVersionLabel: "v1.2.0+build.1",
		"neutree.ai/accelerator-type": "cuda",
	}, nil).Once()
	images.On("CheckImageExists", mock.Anything, mock.Anything, false).Return(true, nil).Times(3)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/clusters/available_versions?workspace=default&image_registry=registry&cluster_type=ssh", nil)

	getAvailableClusterVersions(&Dependencies{
		Storage:             store,
		ReleaseInfoProvider: &testReleaseInfoProvider{info: semanticReleaseInfo()},
		ImageService:        images,
	})(context)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response availableClusterVersionsResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, []string{"v1.2.0+build.1"}, response.AvailableVersions)
	assert.Nil(t, response.DefaultClusterVersion)
}

func TestGetAvailableClusterVersionsRequiresTargetQueries(t *testing.T) {
	for _, query := range []string{"cluster_type=ssh", "workspace=default&cluster_type=ssh", "workspace=default&image_registry=registry"} {
		t.Run(query, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodGet, "/clusters/available_versions?"+query, nil)
			getAvailableClusterVersions(&Dependencies{})(context)
			assert.Equal(t, http.StatusBadRequest, recorder.Code)
		})
	}
}

func testImageRegistry() v1.ImageRegistry {
	return v1.ImageRegistry{Metadata: &v1.Metadata{Name: "registry", Workspace: "default"}, Spec: &v1.ImageRegistrySpec{URL: "https://registry.example.com", Repository: "neutree"}}
}

type testReleaseInfoProvider struct {
	info *v1.ReleaseInfo
	err  error
}

func (provider *testReleaseInfoProvider) Current() (*v1.ReleaseInfo, error) {
	return provider.info, provider.err
}

func completeClusterProfile(version string) *v1.ClusterProfile {
	return &v1.ClusterProfile{APIVersion: "v1", Kind: v1.ClusterProfileKind, Metadata: &v1.Metadata{Name: version}, Spec: &v1.ClusterProfileSpec{Components: map[string]v1.ClusterProfileComponents{
		v1.SSHClusterType:        {RayRuntime: v1.ImageRef{Image: "neutree/neutree-serve", Tag: version}, NodeAgent: v1.ImageRef{Image: "neutree/node-agent", Tag: version}, NodeExporter: v1.ImageRef{Image: "node-exporter", Tag: "v1"}, VMAgent: v1.ImageRef{Image: "vmagent", Tag: "v1"}},
		v1.KubernetesClusterType: {KubernetesRuntime: v1.ImageRef{Image: "neutree/runtime", Tag: version}, Router: v1.ImageRef{Image: "neutree/router", Tag: version}, NodeAgent: v1.ImageRef{Image: "neutree/node-agent", Tag: version}, NodeExporter: v1.ImageRef{Image: "node-exporter", Tag: "v1"}, VMAgent: v1.ImageRef{Image: "vmagent", Tag: "v1"}, KubeStateMetrics: v1.ImageRef{Image: "kube-state", Tag: "v1"}},
	}}}
}
