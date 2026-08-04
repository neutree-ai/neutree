package clusters

import (
	"encoding/json"
	"errors"
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

func createTestContextWithQuery(queryParams map[string]string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	query := req.URL.Query()
	for key, value := range queryParams {
		query.Set(key, value)
	}
	req.URL.RawQuery = query.Encode()
	c.Request = req

	return c, w
}

func TestGetAvailableClusterVersionsUsesReleaseInfoComponents(t *testing.T) {
	store := storageMocks.NewMockStorage(t)
	imageService := registryMocks.NewMockImageService(t)
	store.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{testImageRegistry("https://registry.example.com", "project")}, nil)

	for _, image := range []string{
		"registry.example.com/project/neutree/neutree-serve:v1.1.1",
		"registry.example.com/project/neutree/neutree-node-agent:v1.1.0-rc.1",
		"registry.example.com/project/prometheus/node-exporter:v1.8.2",
		"registry.example.com/project/victoriametrics/vmagent:v1.115.0",
		"registry.example.com/project/nvidia/k8s/dcgm-exporter:4.5.3-4.8.2-distroless",
	} {
		imageService.On("CheckImageExists", image, mock.Anything, false).Return(true, nil)
	}

	context, recorder := createTestContextWithQuery(map[string]string{
		"workspace":        "default",
		"image_registry":   "registry",
		"cluster_type":     "ssh",
		"accelerator_type": "nvidia_gpu",
	})
	getAvailableClusterVersions(&Dependencies{
		Storage:             store,
		ImageService:        imageService,
		ReleaseInfoProvider: &testReleaseInfoProvider{info: testReleaseInfo()},
	})(context)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response availableClusterVersionsResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, []string{"v1.2.0"}, response.AvailableVersions)
	imageService.AssertExpectations(t)
}

func TestGetAvailableClusterVersionsOmitsVersionWithMissingSelectedAcceleratorImage(t *testing.T) {
	store := storageMocks.NewMockStorage(t)
	imageService := registryMocks.NewMockImageService(t)
	store.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{testImageRegistry("registry.example.com", "")}, nil)
	imageService.On("CheckImageExists", "registry.example.com/neutree/neutree-serve:v1.1.1-rocm", mock.Anything, false).Return(false, nil)
	imageService.On("CheckImageExists", mock.Anything, mock.Anything, false).Return(true, nil)

	context, recorder := createTestContextWithQuery(map[string]string{
		"workspace":        "default",
		"image_registry":   "registry",
		"cluster_type":     "kubernetes",
		"accelerator_type": "amd_gpu",
	})
	getAvailableClusterVersions(&Dependencies{
		Storage:             store,
		ImageService:        imageService,
		ReleaseInfoProvider: &testReleaseInfoProvider{info: testReleaseInfo()},
	})(context)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response availableClusterVersionsResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Empty(t, response.AvailableVersions)
}

func TestGetAvailableClusterVersionsPassesHTTPRegistryScheme(t *testing.T) {
	store := storageMocks.NewMockStorage(t)
	imageService := registryMocks.NewMockImageService(t)
	store.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{testImageRegistry("http://registry.example.com:5000", "")}, nil)
	imageService.On("CheckImageExists", mock.Anything, mock.Anything, true).Return(true, nil)

	context, recorder := createTestContextWithQuery(map[string]string{
		"workspace":      "default",
		"image_registry": "registry",
		"cluster_type":   "ssh",
	})
	getAvailableClusterVersions(&Dependencies{
		Storage:             store,
		ImageService:        imageService,
		ReleaseInfoProvider: &testReleaseInfoProvider{info: testReleaseInfo()},
	})(context)

	assert.Equal(t, http.StatusOK, recorder.Code)
	imageService.AssertExpectations(t)
}

func TestRequiredReleaseImagesScopesComponentsByClusterType(t *testing.T) {
	version := v1.ReleaseInfoClusterVersion{
		Components: map[string]string{
			"ray_runtime":        "neutree/neutree-serve:v1.1.1",
			"router":             "neutree/router:v1.1.1",
			"node_agent":         "neutree/neutree-node-agent:v1.1.0-rc.1",
			"node_exporter":      "quay.io/prometheus/node-exporter:v1.8.2",
			"vmagent":            "victoriametrics/vmagent:v1.115.0",
			"kube_state_metrics": "registry.k8s.io/kube-state-metrics/kube-state-metrics:v2.15.0",
		},
		AcceleratorComponents: map[string]map[string]string{
			"nvidia_gpu": {"dcgm_exporter": "nvcr.io/nvidia/k8s/dcgm-exporter:4.5.3-4.8.2-distroless"},
		},
	}

	assert.ElementsMatch(t, []string{
		"neutree/neutree-serve:v1.1.1",
		"neutree/neutree-node-agent:v1.1.0-rc.1",
		"quay.io/prometheus/node-exporter:v1.8.2",
		"victoriametrics/vmagent:v1.115.0",
		"nvcr.io/nvidia/k8s/dcgm-exporter:4.5.3-4.8.2-distroless",
	}, requiredReleaseImages(version, string(v1.SSHClusterType), "nvidia_gpu"))

	assert.ElementsMatch(t, []string{
		"neutree/neutree-serve:v1.1.1",
		"neutree/router:v1.1.1",
		"neutree/neutree-node-agent:v1.1.0-rc.1",
		"quay.io/prometheus/node-exporter:v1.8.2",
		"victoriametrics/vmagent:v1.115.0",
		"registry.k8s.io/kube-state-metrics/kube-state-metrics:v2.15.0",
		"nvcr.io/nvidia/k8s/dcgm-exporter:4.5.3-4.8.2-distroless",
	}, requiredReleaseImages(version, string(v1.KubernetesClusterType), "nvidia_gpu"))
}

func TestGetAvailableClusterVersionsRejectsUnavailableDependencies(t *testing.T) {
	tests := []struct {
		name        string
		query       map[string]string
		providerErr error
		registries  []v1.ImageRegistry
		wantStatus  int
		wantError   string
	}{
		{
			name:       "missing workspace",
			query:      map[string]string{"image_registry": "registry", "cluster_type": "ssh"},
			wantStatus: http.StatusBadRequest,
			wantError:  "workspace is required",
		},
		{
			name:       "unsupported cluster type",
			query:      map[string]string{"workspace": "default", "image_registry": "registry", "cluster_type": "unsupported"},
			wantStatus: http.StatusBadRequest,
			wantError:  "unsupported cluster_type",
		},
		{
			name:        "release info is unavailable",
			query:       map[string]string{"workspace": "default", "image_registry": "registry", "cluster_type": "ssh"},
			providerErr: errors.New("release info v1.2.0 not found"),
			wantStatus:  http.StatusInternalServerError,
			wantError:   "failed to get release info",
		},
		{
			name:       "image registry is unavailable",
			query:      map[string]string{"workspace": "default", "image_registry": "registry", "cluster_type": "ssh"},
			registries: []v1.ImageRegistry{},
			wantStatus: http.StatusNotFound,
			wantError:  "image registry default/registry not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := storageMocks.NewMockStorage(t)
			imageService := registryMocks.NewMockImageService(t)
			if tt.registries != nil {
				store.On("ListImageRegistry", mock.Anything).Return(tt.registries, nil)
			}

			context, recorder := createTestContextWithQuery(tt.query)
			getAvailableClusterVersions(&Dependencies{
				Storage:             store,
				ImageService:        imageService,
				ReleaseInfoProvider: &testReleaseInfoProvider{info: testReleaseInfo(), err: tt.providerErr},
			})(context)

			assert.Equal(t, tt.wantStatus, recorder.Code)
			var response map[string]string
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
			assert.Contains(t, response["error"], tt.wantError)
		})
	}
}

func TestGetVersionMatrixDoesNotExposeComponentReferences(t *testing.T) {
	context, recorder := createTestContextWithQuery(map[string]string{"workspace": "default"})
	getVersionMatrix(&Dependencies{ReleaseInfoProvider: &testReleaseInfoProvider{info: testReleaseInfo()}})(context)

	require.Equal(t, http.StatusOK, recorder.Code)
	assert.JSONEq(t, `{"versions":[{"version":"v1.2.0","state":"Active","upgrade_to":[]}]}`, recorder.Body.String())
	assert.NotContains(t, recorder.Body.String(), "neutree-serve")
	assert.NotContains(t, recorder.Body.String(), "build_identity")
}

func TestGetClusterUpgradePreflightUsesCurrentReleaseInfoEdge(t *testing.T) {
	store := storageMocks.NewMockStorage(t)
	store.On("ListCluster", mock.Anything).Return([]v1.Cluster{{
		Metadata: &v1.Metadata{Name: "cluster-a", Workspace: "default"},
		Spec:     &v1.ClusterSpec{Version: "v1.1.0"},
		Status:   &v1.ClusterStatus{Version: "v1.1.0"},
	}}, nil).Once()
	context, recorder := createTestContextWithQuery(map[string]string{
		"workspace":      "default",
		"name":           "cluster-a",
		"target_version": "v1.2.0",
	})

	getClusterUpgradePreflight(&Dependencies{
		Storage:             store,
		ReleaseInfoProvider: &testReleaseInfoProvider{info: testUpgradeReleaseInfo()},
	})(context)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response clusterUpgradePreflightResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.True(t, response.Allowed)
	assert.Equal(t, "v1.1.0", response.SourceVersion)
	assert.Equal(t, "v1.2.0", response.TargetVersion)
	assert.Equal(t, []string{"v1.1.1", "v1.2.0"}, response.UpgradeTo)
	assert.Equal(t, "v1.2.0", response.ReleaseInfo.Baseline)
	assert.Equal(t, "revision-2", response.ReleaseInfo.Revision)
	store.AssertExpectations(t)
}

func TestGetClusterUpgradePreflightRejectsUndeclaredEdge(t *testing.T) {
	store := storageMocks.NewMockStorage(t)
	store.On("ListCluster", mock.Anything).Return([]v1.Cluster{{
		Metadata: &v1.Metadata{Name: "cluster-a", Workspace: "default"},
		Spec:     &v1.ClusterSpec{Version: "v1.1.1"},
		Status:   &v1.ClusterStatus{Version: "v1.1.1"},
	}}, nil).Once()
	context, recorder := createTestContextWithQuery(map[string]string{
		"workspace":      "default",
		"name":           "cluster-a",
		"target_version": "v1.1.0",
	})

	getClusterUpgradePreflight(&Dependencies{
		Storage:             store,
		ReleaseInfoProvider: &testReleaseInfoProvider{info: testUpgradeReleaseInfo()},
	})(context)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "not allowed")
	store.AssertExpectations(t)
}

type testReleaseInfoProvider struct {
	info *v1.ReleaseInfo
	err  error
}

func (provider *testReleaseInfoProvider) Current() (*v1.ReleaseInfo, error) {
	return provider.info, provider.err
}

func testReleaseInfo() *v1.ReleaseInfo {
	return &v1.ReleaseInfo{
		Metadata: &v1.Metadata{Name: "v1.2.0"},
		Spec: &v1.ReleaseInfoSpec{
			BuildIdentity: "v1.2.0",
			ClusterVersions: []v1.ReleaseInfoClusterVersion{{
				Version: "v1.2.0",
				State:   v1.ReleaseInfoClusterVersionStateActive,
				Components: map[string]string{
					"ray_runtime":        "neutree/neutree-serve:v1.1.1",
					"router":             "neutree/router:v1.1.1",
					"node_agent":         "neutree/neutree-node-agent:v1.1.0-rc.1",
					"node_exporter":      "quay.io/prometheus/node-exporter:v1.8.2",
					"vmagent":            "victoriametrics/vmagent:v1.115.0",
					"kube_state_metrics": "registry.k8s.io/kube-state-metrics/kube-state-metrics:v2.15.0",
				},
				AcceleratorComponents: map[string]map[string]string{
					"nvidia_gpu": {"dcgm_exporter": "nvcr.io/nvidia/k8s/dcgm-exporter:4.5.3-4.8.2-distroless"},
					"amd_gpu":    {"ray_runtime": "neutree/neutree-serve:v1.1.1-rocm"},
				},
			}},
		},
	}
}

func testUpgradeReleaseInfo() *v1.ReleaseInfo {
	return &v1.ReleaseInfo{
		Metadata: &v1.Metadata{Name: "v1.2.0"},
		Spec: &v1.ReleaseInfoSpec{ClusterVersions: []v1.ReleaseInfoClusterVersion{
			{
				Version:   "v1.1.0",
				State:     v1.ReleaseInfoClusterVersionStateActive,
				UpgradeTo: []string{"v1.1.1", "v1.2.0"},
			},
			{
				Version:   "v1.1.1",
				State:     v1.ReleaseInfoClusterVersionStateActive,
				UpgradeTo: []string{"v1.2.0"},
			},
			{Version: "v1.2.0", State: v1.ReleaseInfoClusterVersionStateActive},
		}},
		Status: &v1.ReleaseInfoStatus{Revision: "revision-2"},
	}
}

func testImageRegistry(url, repository string) v1.ImageRegistry {
	return v1.ImageRegistry{Spec: &v1.ImageRegistrySpec{URL: url, Repository: repository}}
}
