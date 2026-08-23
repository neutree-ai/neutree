package clusters

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/storage"
	storageMocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

const availableVersionsImagePrefix = "registry.example.com/project"

func TestGetAvailableClusterVersions(t *testing.T) {
	tests := []struct {
		name         string
		clusterType  string
		profiles     []v1.ClusterProfile
		registryURL  string
		configure    func(*availableVersionsImageService, []v1.ClusterProfile)
		wantStatus   int
		wantVersions []string
		wantDefault  *string
		verifyCalls  func(*testing.T, *availableVersionsImageService)
	}{
		{
			name:        "SSH returns eligible profiles with base images and accelerator labels",
			clusterType: v1.SSHClusterType,
			profiles: []v1.ClusterProfile{
				availableVersionsProfile("v1.1.1"),
				availableVersionsProfile("v1.2.0"),
				availableVersionsProfile("v1.2.1"),
			},
			configure: func(images *availableVersionsImageService, profiles []v1.ClusterProfile) {
				configureAvailableSSHProfile(images, profiles[0], true)
				configureAvailableSSHProfile(images, profiles[1], true)
			},
			wantStatus:   http.StatusOK,
			wantVersions: []string{"v1.1.1", "v1.2.0"},
			wantDefault:  stringPointer("v1.2.0"),
			verifyCalls: func(t *testing.T, images *availableVersionsImageService) {
				assert.Len(t, images.tagCalls, 1)
				assert.Len(t, images.labelCalls, 2)
			},
		},
		{
			name:        "missing default material leaves compatible lower version and null default",
			clusterType: v1.SSHClusterType,
			profiles: []v1.ClusterProfile{
				availableVersionsProfile("v1.1.1"),
				availableVersionsProfile("v1.2.0"),
			},
			configure: func(images *availableVersionsImageService, profiles []v1.ClusterProfile) {
				configureAvailableSSHProfile(images, profiles[0], true)
				configureAvailableSSHProfile(images, profiles[1], true)
				ssh, _ := profiles[1].Spec.ComponentsFor(v1.SSHClusterType)
				images.existence[availableVersionsImageRef(ssh.NodeAgent)] = availableVersionsExistenceResult{}
			},
			wantStatus:   http.StatusOK,
			wantVersions: []string{"v1.1.1"},
		},
		{
			name:        "SSH requires a nonempty accelerator label",
			clusterType: v1.SSHClusterType,
			profiles:    []v1.ClusterProfile{availableVersionsProfile("v1.2.0")},
			configure: func(images *availableVersionsImageService, profiles []v1.ClusterProfile) {
				configureAvailableSSHProfile(images, profiles[0], false)
			},
			wantStatus:   http.StatusOK,
			wantVersions: []string{},
		},
		{
			name:        "SSH ignores accelerator labels for another cluster version",
			clusterType: v1.SSHClusterType,
			profiles:    []v1.ClusterProfile{availableVersionsProfile("v1.2.0")},
			configure: func(images *availableVersionsImageService, profiles []v1.ClusterProfile) {
				configureAvailableSSHProfile(images, profiles[0], true)
				ssh, _ := profiles[0].Spec.ComponentsFor(v1.SSHClusterType)
				runtimeRepository := util.RewriteImageRef(availableVersionsImagePrefix, ssh.RayRuntime.Image)
				tag := "accelerator-" + profiles[0].GetName()
				images.labels[runtimeRepository+":"+tag] = availableVersionsLabelsResult{labels: map[string]string{
					v1.ImageLabelVersion:         "v1.1.1",
					v1.ImageLabelAcceleratorType: "cuda",
				}}
			},
			wantStatus:   http.StatusOK,
			wantVersions: []string{},
		},
		{
			name:        "SSH uses the profile ray runtime repository for accelerator labels",
			clusterType: v1.SSHClusterType,
			profiles: []v1.ClusterProfile{
				func() v1.ClusterProfile {
					profile := availableVersionsProfile("v1.2.0")
					ssh, _ := profile.Spec.ComponentsFor(v1.SSHClusterType)
					ssh.RayRuntime.Image = "enterprise/neutree-serve"
					profile.Spec.Components[v1.SSHClusterType] = ssh

					return profile
				}(),
			},
			configure: func(images *availableVersionsImageService, profiles []v1.ClusterProfile) {
				configureAvailableSSHProfile(images, profiles[0], true)
			},
			wantStatus:   http.StatusOK,
			wantVersions: []string{"v1.2.0"},
			wantDefault:  stringPointer("v1.2.0"),
		},
		{
			name:        "Kubernetes requires its complete base matrix but no accelerator query",
			clusterType: v1.KubernetesClusterType,
			profiles:    []v1.ClusterProfile{availableVersionsProfile("v1.2.0")},
			configure: func(images *availableVersionsImageService, profiles []v1.ClusterProfile) {
				configureAvailableKubernetesProfile(images, profiles[0])
			},
			wantStatus:   http.StatusOK,
			wantVersions: []string{"v1.2.0"},
			wantDefault:  stringPointer("v1.2.0"),
			verifyCalls: func(t *testing.T, images *availableVersionsImageService) {
				assert.Empty(t, images.tagCalls)
				assert.Empty(t, images.labelCalls)
			},
		},
		{
			name:        "Kubernetes registry error fails instead of becoming an empty result",
			clusterType: v1.KubernetesClusterType,
			profiles:    []v1.ClusterProfile{availableVersionsProfile("v1.2.0")},
			configure: func(images *availableVersionsImageService, profiles []v1.ClusterProfile) {
				configureAvailableKubernetesProfile(images, profiles[0])
				components, _ := profiles[0].Spec.ComponentsFor(v1.KubernetesClusterType)
				images.existence[availableVersionsImageRef(components.Router)] = availableVersionsExistenceResult{
					err: errors.New("registry unavailable"),
				}
			},
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:        "SSH label lookup failure fails instead of excluding the candidate",
			clusterType: v1.SSHClusterType,
			profiles:    []v1.ClusterProfile{availableVersionsProfile("v1.2.0")},
			configure: func(images *availableVersionsImageService, profiles []v1.ClusterProfile) {
				configureAvailableSSHProfile(images, profiles[0], true)
				ssh, _ := profiles[0].Spec.ComponentsFor(v1.SSHClusterType)
				runtimeRepository := util.RewriteImageRef(availableVersionsImagePrefix, ssh.RayRuntime.Image)
				tag := "accelerator-" + profiles[0].GetName()
				images.labels[runtimeRepository+":"+tag] = availableVersionsLabelsResult{
					err: errors.New("registry label lookup failed"),
				}
			},
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:        "eligible prerelease remains distinct from default identity",
			clusterType: v1.KubernetesClusterType,
			profiles:    []v1.ClusterProfile{availableVersionsProfile("v1.2.0-alpha.1")},
			registryURL: "http://registry.example.com",
			configure: func(images *availableVersionsImageService, profiles []v1.ClusterProfile) {
				configureAvailableKubernetesProfile(images, profiles[0])
			},
			wantStatus:   http.StatusOK,
			wantVersions: []string{"v1.2.0-alpha.1"},
			verifyCalls: func(t *testing.T, images *availableVersionsImageService) {
				require.NotEmpty(t, images.existenceCalls)
				for _, call := range images.existenceCalls {
					assert.True(t, call.useHTTP)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			images := newAvailableVersionsImageService()
			if tt.configure != nil {
				tt.configure(images, tt.profiles)
			}

			deps := availableVersionsDependencies(t, tt.profiles, tt.registryURL, images)
			response := executeAvailableVersions(
				t,
				deps,
				"workspace=default&image_registry=registry&cluster_type="+tt.clusterType,
			)

			assert.Equal(t, tt.wantStatus, response.Code)
			if tt.wantStatus == http.StatusOK {
				var body availableClusterVersionsResponse
				require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
				assert.Equal(t, tt.wantVersions, body.AvailableVersions)
				assert.Equal(t, tt.wantDefault, body.DefaultClusterVersion)
			}
			if tt.wantStatus >= http.StatusInternalServerError {
				assert.Contains(t, response.Body.String(), "internal server error")
				assert.NotContains(t, response.Body.String(), "registry")
			}

			if tt.verifyCalls != nil {
				tt.verifyCalls(t, images)
			}
		})
	}
}

func TestGetAvailableClusterVersionsRejectsIncompleteTarget(t *testing.T) {
	for _, query := range []string{
		"cluster_type=ssh",
		"workspace=default&cluster_type=ssh",
		"workspace=default&image_registry=registry",
		"workspace=default&image_registry=registry&cluster_type=invalid",
	} {
		t.Run(query, func(t *testing.T) {
			response := executeAvailableVersions(t, &Dependencies{}, query)
			assert.Equal(t, http.StatusBadRequest, response.Code)
		})
	}
}

func availableVersionsDependencies(
	t *testing.T,
	profiles []v1.ClusterProfile,
	registryURL string,
	images *availableVersionsImageService,
) *Dependencies {
	t.Helper()
	if registryURL == "" {
		registryURL = "https://registry.example.com"
	}

	store := storageMocks.NewMockStorage(t)
	store.On("ListImageRegistry", mock.MatchedBy(matchesAvailableVersionsRegistryFilter)).
		Return([]v1.ImageRegistry{availableVersionsImageRegistry(registryURL)}, nil).Once()
	store.On("ListClusterProfile", mock.Anything).Return(profiles, nil).Once()

	return &Dependencies{
		Storage:             store,
		ReleaseInfoProvider: availableVersionsReleaseInfoProvider{info: availableVersionsReleaseInfo()},
		ImageService:        images,
	}
}

func executeAvailableVersions(t *testing.T, deps *Dependencies, query string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/clusters/available_versions?"+query, nil)
	getAvailableClusterVersions(deps)(context)

	return recorder
}

func matchesAvailableVersionsRegistryFilter(option storage.ListOption) bool {
	if len(option.Filters) != 2 {
		return false
	}

	return option.Filters[0] == (storage.Filter{Column: "metadata->>workspace", Operator: "eq", Value: "default"}) &&
		option.Filters[1] == (storage.Filter{Column: "metadata->>name", Operator: "eq", Value: "registry"})
}

func availableVersionsReleaseInfo() *v1.ReleaseInfo {
	return &v1.ReleaseInfo{
		APIVersion: "v1",
		Kind:       v1.ReleaseInfoKind,
		Metadata:   &v1.Metadata{Name: "v1.2.0"},
		Spec: &v1.ReleaseInfoSpec{
			DefaultClusterVersion:      "v1.2.0",
			CompatibleClusterBaselines: []string{"v1.1", "v1.2"},
		},
	}
}

func availableVersionsProfile(version string) v1.ClusterProfile {
	return v1.ClusterProfile{
		APIVersion: "v1",
		Kind:       v1.ClusterProfileKind,
		Metadata:   &v1.Metadata{Name: version},
		Spec: &v1.ClusterProfileSpec{Components: map[string]v1.ClusterProfileComponents{
			v1.SSHClusterType: {
				RayRuntime:   v1.ImageRef{Image: v1.NeutreeServeImageName, Tag: version},
				NodeAgent:    v1.ImageRef{Image: "neutree/node-agent", Tag: version},
				NodeExporter: v1.ImageRef{Image: "prometheus/node-exporter", Tag: "v1.8.2"},
				VMAgent:      v1.ImageRef{Image: "victoriametrics/vmagent", Tag: "v1.111.0"},
			},
			v1.KubernetesClusterType: {
				KubernetesRuntime: v1.ImageRef{Image: "neutree/kubernetes-runtime", Tag: version},
				Router:            v1.ImageRef{Image: v1.NeutreeRouterImageName, Tag: version},
				NodeAgent:         v1.ImageRef{Image: "neutree/node-agent", Tag: version},
				NodeExporter:      v1.ImageRef{Image: "prometheus/node-exporter", Tag: "v1.8.2"},
				VMAgent:           v1.ImageRef{Image: "victoriametrics/vmagent", Tag: "v1.111.0"},
				KubeStateMetrics:  v1.ImageRef{Image: "kubernetes/kube-state-metrics", Tag: "v2.15.0"},
			},
		}},
	}
}

func availableVersionsImageRegistry(url string) v1.ImageRegistry {
	return v1.ImageRegistry{
		Metadata: &v1.Metadata{Name: "registry", Workspace: "default"},
		Spec: &v1.ImageRegistrySpec{
			URL:        url,
			Repository: "project",
		},
	}
}

func configureAvailableSSHProfile(images *availableVersionsImageService, profile v1.ClusterProfile, accelerator bool) {
	components, _ := profile.Spec.ComponentsFor(v1.SSHClusterType)
	for _, ref := range []v1.ImageRef{components.NodeAgent, components.NodeExporter, components.VMAgent} {
		images.existence[availableVersionsImageRef(ref)] = availableVersionsExistenceResult{exists: true}
	}

	serveRepository := util.RewriteImageRef(availableVersionsImagePrefix, components.RayRuntime.Image)
	tag := "accelerator-" + profile.GetName()
	images.tags[serveRepository] = availableVersionsTagsResult{tags: append(images.tags[serveRepository].tags, tag)}
	labels := map[string]string{v1.ImageLabelVersion: profile.GetName()}
	if accelerator {
		labels[v1.ImageLabelAcceleratorType] = "cuda"
	}
	images.labels[serveRepository+":"+tag] = availableVersionsLabelsResult{labels: labels}
}

func configureAvailableKubernetesProfile(images *availableVersionsImageService, profile v1.ClusterProfile) {
	components, _ := profile.Spec.ComponentsFor(v1.KubernetesClusterType)
	for _, ref := range []v1.ImageRef{
		components.KubernetesRuntime,
		components.Router,
		components.NodeAgent,
		components.NodeExporter,
		components.VMAgent,
		components.KubeStateMetrics,
	} {
		images.existence[availableVersionsImageRef(ref)] = availableVersionsExistenceResult{exists: true}
	}
}

func availableVersionsImageRef(ref v1.ImageRef) string {
	return util.RewriteImageRef(availableVersionsImagePrefix, ref.Image+":"+ref.Tag)
}

func stringPointer(value string) *string {
	return &value
}

type availableVersionsReleaseInfoProvider struct {
	info *v1.ReleaseInfo
	err  error
}

func (provider availableVersionsReleaseInfoProvider) Current() (*v1.ReleaseInfo, error) {
	return provider.info, provider.err
}

type availableVersionsImageService struct {
	existence      map[string]availableVersionsExistenceResult
	tags           map[string]availableVersionsTagsResult
	labels         map[string]availableVersionsLabelsResult
	existenceCalls []availableVersionsImageCall
	tagCalls       []availableVersionsImageCall
	labelCalls     []availableVersionsImageCall
}

type availableVersionsExistenceResult struct {
	exists bool
	err    error
}

type availableVersionsTagsResult struct {
	tags []string
	err  error
}

type availableVersionsLabelsResult struct {
	labels map[string]string
	err    error
}

type availableVersionsImageCall struct {
	image   string
	useHTTP bool
}

func newAvailableVersionsImageService() *availableVersionsImageService {
	return &availableVersionsImageService{
		existence: map[string]availableVersionsExistenceResult{},
		tags:      map[string]availableVersionsTagsResult{},
		labels:    map[string]availableVersionsLabelsResult{},
	}
}

func (service *availableVersionsImageService) CheckImageExists(
	image string,
	_ authn.Authenticator,
	useHTTP bool,
) (bool, error) {
	service.existenceCalls = append(service.existenceCalls, availableVersionsImageCall{image: image, useHTTP: useHTTP})
	result, found := service.existence[image]
	if !found {
		return true, nil
	}

	return result.exists, result.err
}

func (service *availableVersionsImageService) CheckPullPermission(
	_ string,
	_ authn.Authenticator,
	_ bool,
) (bool, error) {
	return true, nil
}

func (service *availableVersionsImageService) ListImageTags(
	image string,
	_ authn.Authenticator,
	useHTTP bool,
) ([]string, error) {
	service.tagCalls = append(service.tagCalls, availableVersionsImageCall{image: image, useHTTP: useHTTP})
	result := service.tags[image]

	return result.tags, result.err
}

func (service *availableVersionsImageService) GetImageLabels(
	image string,
	_ authn.Authenticator,
	useHTTP bool,
) (map[string]string, error) {
	service.labelCalls = append(service.labelCalls, availableVersionsImageCall{image: image, useHTTP: useHTTP})
	result := service.labels[image]

	return result.labels, result.err
}
