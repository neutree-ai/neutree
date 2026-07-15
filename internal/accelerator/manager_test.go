package accelerator

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
	"github.com/neutree-ai/neutree/internal/accelerator/resourceparser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManagerGetAcceleratorProfile(t *testing.T) {
	m := &manager{}
	m.acceleratorsMap.Store(v1.AcceleratorTypeNVIDIAGPU.String(), registerPlugin{
		resource:         v1.AcceleratorTypeNVIDIAGPU.String(),
		plugin:           &plugin.GPUAcceleratorPlugin{},
		lastRegisterTime: time.Now(),
	})

	profile, err := m.GetAcceleratorProfile(context.Background(), v1.AcceleratorTypeNVIDIAGPU.String())

	require.NoError(t, err)
	require.NotNil(t, profile)
	assert.Equal(t, v1.AcceleratorTypeNVIDIAGPU.String(), profile.AcceleratorType)
	require.NotNil(t, profile.MetricsExporter)
	assert.Equal(t, "dcgm-exporter", profile.MetricsExporter.Name)
}

func TestManagerGetAcceleratorProfileUsesTypeAwareResolver(t *testing.T) {
	m := &manager{}
	m.acceleratorsMap.Store("npu", registerPlugin{
		resource: "npu",
		plugin: &fakeStaticNodeAcceleratorPlugin{acceleratorProfiles: map[string]*v1.AcceleratorProfile{
			"npu-ascend910b": {
				AcceleratorType: "npu-ascend910b",
				ClusterRuntime:  &v1.RuntimeConfig{ImageSuffix: "npu-ascend910b", Runtime: "ascend"},
			},
		},
		},
		lastRegisterTime: time.Now(),
	})

	profile, err := m.GetAcceleratorProfile(context.Background(), "npu-ascend910b")

	require.NoError(t, err)
	require.NotNil(t, profile)
	assert.Equal(t, "npu-ascend910b", profile.AcceleratorType)
	assert.Equal(t, "npu-ascend910b", profile.ClusterRuntime.ImageSuffix)
}

func TestNewManagerRegistersInjectedInternalPlugin(t *testing.T) {
	injected := &fakeStaticNodeAcceleratorPlugin{}

	m, err := NewManagerWithPlugins(gin.New(), injected)

	require.NoError(t, err)
	assert.Contains(t, m.SupportPlugins(), injected.Resource())
}

func TestNewManagerCreatesDefaultPlugins(t *testing.T) {
	m := NewManager(gin.New())

	require.NotNil(t, m)
	assert.NotEmpty(t, m.SupportPlugins())
}

func TestNewManagerWithPluginsRejectsInvalidInternalPlugins(t *testing.T) {
	_, err := NewManagerWithPlugins(gin.New(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accelerator plugin is nil")

	emptyResource := &fakeStaticNodeAcceleratorPlugin{resourceSet: true}
	_, err = NewManagerWithPlugins(gin.New(), emptyResource)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resource is required")

	injected := &fakeStaticNodeAcceleratorPlugin{}
	_, err = NewManagerWithPlugins(gin.New(), injected, injected)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already registered")
}

func TestManagerGetAcceleratorProfileFromExternalPlugin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, v1.GetAcceleratorProfilePath, r.URL.Path)

		err := json.NewEncoder(w).Encode(v1.GetAcceleratorProfileResponse{
			Profile: v1.AcceleratorProfile{
				AcceleratorType: "external_gpu",
				ClusterRuntime: &v1.RuntimeConfig{
					Runtime: "custom-cluster",
				},
				EngineRuntime: &v1.RuntimeConfig{
					Runtime: "custom-engine",
				},
			},
		})
		require.NoError(t, err)
	}))
	defer server.Close()

	m := &manager{}
	m.acceleratorsMap.Store("external_gpu", registerPlugin{
		resource:         "external_gpu",
		plugin:           plugin.NewAcceleratorRestPlugin("external_gpu", server.URL),
		lastRegisterTime: time.Now(),
	})

	profile, err := m.GetAcceleratorProfile(context.Background(), "external_gpu")

	require.NoError(t, err)
	require.NotNil(t, profile)
	assert.Equal(t, "external_gpu", profile.AcceleratorType)
	require.NotNil(t, profile.ClusterRuntime)
	assert.Equal(t, "custom-cluster", profile.ClusterRuntime.Runtime)
	require.NotNil(t, profile.EngineRuntime)
	assert.Equal(t, "custom-engine", profile.EngineRuntime.Runtime)
}

func TestManagerGetAcceleratorProfileNotFoundReturnsError(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	m := &manager{}
	m.acceleratorsMap.Store("external_gpu", registerPlugin{
		resource:         "external_gpu",
		plugin:           plugin.NewAcceleratorRestPlugin("external_gpu", server.URL),
		lastRegisterTime: time.Now(),
	})

	profile, err := m.GetAcceleratorProfile(context.Background(), "external_gpu")

	assert.Nil(t, profile)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get accelerator profile from plugin external_gpu failed")
}

func TestManagerGetAcceleratorProfileMissingPlugin(t *testing.T) {
	m := &manager{}

	profile, err := m.GetAcceleratorProfile(context.Background(), "missing")

	require.Error(t, err)
	assert.Nil(t, profile)
	assert.Contains(t, err.Error(), "accelerator plugin missing not found")
}

func TestManagerGetAcceleratorProfileReturnsTypeResolverError(t *testing.T) {
	m := &manager{}
	m.acceleratorsMap.Store("npu", registerPlugin{
		resource: "npu",
		plugin:   &fakeStaticNodeAcceleratorPlugin{profileResolverErr: errors.New("profile lookup failed")},
	})

	profile, err := m.GetAcceleratorProfile(context.Background(), "npu-ascend910b")

	require.Error(t, err)
	assert.Nil(t, profile)
	assert.Contains(t, err.Error(), "profile lookup failed")
}

func TestManagerGetAcceleratorProfileReturnsNotFoundWhenTypeResolverDoesNotMatch(t *testing.T) {
	m := &manager{}
	m.acceleratorsMap.Store("npu", registerPlugin{
		resource: "npu",
		plugin:   &fakeStaticNodeAcceleratorPlugin{},
	})

	profile, err := m.GetAcceleratorProfile(context.Background(), "npu-ascend910b")

	require.Error(t, err)
	assert.Nil(t, profile)
	assert.Contains(t, err.Error(), "accelerator plugin npu-ascend910b not found")
}

func TestManagerValidateStaticClusterVersion(t *testing.T) {
	m := &manager{}
	m.acceleratorsMap.Store("npu", registerPlugin{
		resource: "npu",
		plugin: &fakeStaticNodeAcceleratorPlugin{
			validationMatched: true,
			validationErr:     errors.New("v1.1.0 required"),
		},
	})

	err := m.ValidateStaticClusterVersion(context.Background(), "npu-ascend910b", "v1.0.2")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "v1.1.0 required")
}

func TestManagerValidateStaticClusterVersionIgnoresUnmatchedType(t *testing.T) {
	m := &manager{}
	m.acceleratorsMap.Store("npu", registerPlugin{
		resource: "npu",
		plugin:   &fakeStaticNodeAcceleratorPlugin{},
	})

	require.NoError(t, m.ValidateStaticClusterVersion(context.Background(), "other", "v1.0.2"))
}

func TestManagerValidateStaticClusterVersionAcceptsMatchedVersion(t *testing.T) {
	m := &manager{}
	m.acceleratorsMap.Store("npu", registerPlugin{
		resource: "npu",
		plugin:   &fakeStaticNodeAcceleratorPlugin{validationMatched: true},
	})

	require.NoError(t, m.ValidateStaticClusterVersion(context.Background(), "npu-ascend910b", "v1.1.0"))
}

func TestManagerDetectAcceleratorUsesNodeAcceleratorProbe(t *testing.T) {
	detector := &fakeStaticNodeAcceleratorPlugin{accelerators: []v1.Accelerator{{ID: "0"}}}
	m := &manager{}
	m.acceleratorsMap.Store("custom_gpu", registerPlugin{
		resource:         "custom_gpu",
		plugin:           detector,
		lastRegisterTime: time.Now(),
	})

	status, err := m.DetectAccelerator(context.Background(), "10.0.0.10", v1.Auth{
		SSHUser:       "root",
		SSHPrivateKey: "key",
	})

	require.NoError(t, err)
	require.NotNil(t, status)
	assert.Equal(t, "custom_gpu", status.Type)
	assert.Empty(t, status.Devices, "type discovery must not report detailed device data")
	assert.Equal(t, 1, detector.getCalls)
	assert.Equal(t, "10.0.0.10", detector.getRequest.NodeIp)
	assert.Equal(t, "root", detector.getRequest.SSHAuth.SSHUser)
}

func TestManagerDetectAcceleratorPreservesStaticNodeAcceleratorType(t *testing.T) {
	detector := &fakeStaticNodeAcceleratorPlugin{staticResponse: &v1.DetectStaticNodeAcceleratorResponse{
		Matched: true,
		Accelerator: &v1.StaticNodeAcceleratorStatus{
			Type:    "npu-ascend910b",
			Devices: []v1.StaticNodeAcceleratorDeviceStatus{{ID: "0", ProductModel: "HUAWEI_Ascend910B"}},
		},
	}}
	m := &manager{}
	m.acceleratorsMap.Store("npu", registerPlugin{resource: "npu", plugin: detector, lastRegisterTime: time.Now()})

	status, err := m.DetectAccelerator(context.Background(), "10.0.0.10", v1.Auth{SSHUser: "root", SSHPrivateKey: "key"})

	require.NoError(t, err)
	require.NotNil(t, status)
	assert.Equal(t, "npu-ascend910b", status.Type)
	require.Len(t, status.Devices, 1)
	assert.Equal(t, "HUAWEI_Ascend910B", status.Devices[0].ProductModel)
}

func TestManagerDetectAcceleratorSupportsExternalPluginWithoutStaticDetectEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == v1.DetectStaticNodeAcceleratorPath {
			http.NotFound(w, r)
			return
		}

		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, v1.GetNodeAcceleratorPath, r.URL.Path)

		err := json.NewEncoder(w).Encode(v1.GetNodeAcceleratorResponse{
			Accelerators: []v1.Accelerator{{ID: "0"}},
		})
		require.NoError(t, err)
	}))
	defer server.Close()

	m := &manager{}
	m.acceleratorsMap.Store("external_gpu", registerPlugin{
		resource:         "external_gpu",
		plugin:           plugin.NewAcceleratorRestPlugin("external_gpu", server.URL),
		lastRegisterTime: time.Now(),
	})

	status, err := m.DetectAccelerator(context.Background(), "10.0.0.10", v1.Auth{
		SSHUser:       "root",
		SSHPrivateKey: "key",
	})

	require.NoError(t, err)
	require.NotNil(t, status)
	assert.Equal(t, "external_gpu", status.Type)
}

func TestManagerDetectAcceleratorFallsBackToCPUWhenDetectorDoesNotMatch(t *testing.T) {
	detector := &fakeStaticNodeAcceleratorPlugin{}
	m := &manager{}
	m.acceleratorsMap.Store("custom_gpu", registerPlugin{
		resource:         "custom_gpu",
		plugin:           detector,
		lastRegisterTime: time.Now(),
	})

	status, err := m.DetectAccelerator(context.Background(), "10.0.0.10", v1.Auth{
		SSHUser:       "root",
		SSHPrivateKey: "key",
	})

	require.NoError(t, err)
	require.NotNil(t, status)
	assert.Equal(t, v1.StaticNodeAcceleratorTypeCPU, status.Type)
	assert.Equal(t, 1, detector.getCalls)
}

func TestManagerDetectAcceleratorReturnsDetectorErrorWhenNothingMatches(t *testing.T) {
	detector := &fakeStaticNodeAcceleratorPlugin{getErr: errors.New("lspci unavailable")}
	m := &manager{}
	m.acceleratorsMap.Store("custom_gpu", registerPlugin{
		resource:         "custom_gpu",
		plugin:           detector,
		lastRegisterTime: time.Now(),
	})

	status, err := m.DetectAccelerator(context.Background(), "10.0.0.10", v1.Auth{
		SSHUser:       "root",
		SSHPrivateKey: "key",
	})

	require.Error(t, err)
	require.Nil(t, status)
	assert.Contains(t, err.Error(), "detect static node accelerator from plugin custom_gpu failed")
	assert.Equal(t, 1, detector.getCalls)
}

type fakeStaticNodeAcceleratorPlugin struct {
	resource            string
	resourceSet         bool
	accelerators        []v1.Accelerator
	acceleratorProfiles map[string]*v1.AcceleratorProfile
	staticResponse      *v1.DetectStaticNodeAcceleratorResponse
	profileResolverErr  error
	validationErr       error
	validationMatched   bool
	getErr              error
	getCalls            int
	getRequest          *v1.GetNodeAcceleratorRequest

	acceleratorProfile   *v1.AcceleratorProfile
	runtimeProfileConfig v1.RuntimeConfig
}

func (p *fakeStaticNodeAcceleratorPlugin) Resource() string {
	if p.resourceSet {
		return p.resource
	}

	return "custom_gpu"
}

func (p *fakeStaticNodeAcceleratorPlugin) Type() string {
	return plugin.InternalPluginType
}

func (p *fakeStaticNodeAcceleratorPlugin) Handle() plugin.AcceleratorPluginHandle {
	return p
}

func (p *fakeStaticNodeAcceleratorPlugin) DetectStaticNodeAccelerator(
	ctx context.Context,
	request *v1.DetectStaticNodeAcceleratorRequest,
) (*v1.DetectStaticNodeAcceleratorResponse, error) {
	if p.staticResponse != nil {
		return p.staticResponse, nil
	}

	return &v1.DetectStaticNodeAcceleratorResponse{}, nil
}

func (p *fakeStaticNodeAcceleratorPlugin) GetNodeAccelerator(
	ctx context.Context,
	request *v1.GetNodeAcceleratorRequest,
) (*v1.GetNodeAcceleratorResponse, error) {
	p.getCalls++
	p.getRequest = request

	return &v1.GetNodeAcceleratorResponse{
		Accelerators: p.accelerators,
	}, p.getErr
}

func (p *fakeStaticNodeAcceleratorPlugin) GetNodeRuntimeConfig(
	ctx context.Context,
	request *v1.GetNodeRuntimeConfigRequest,
) (*v1.GetNodeRuntimeConfigResponse, error) {
	return nil, nil
}

func (p *fakeStaticNodeAcceleratorPlugin) Ping(ctx context.Context) error {
	return nil
}

func (p *fakeStaticNodeAcceleratorPlugin) GetResourceConverter() plugin.ResourceConverter {
	return nil
}

func (p *fakeStaticNodeAcceleratorPlugin) GetResourceParser() resourceparser.ResourceParser {
	return nil
}

func (p *fakeStaticNodeAcceleratorPlugin) GetContainerRuntimeConfig() (v1.RuntimeConfig, error) {
	return v1.RuntimeConfig{}, nil
}

func (p *fakeStaticNodeAcceleratorPlugin) GetAcceleratorProfile(ctx context.Context) (*v1.AcceleratorProfile, error) {
	return p.acceleratorProfile, nil
}

func (p *fakeStaticNodeAcceleratorPlugin) GetAcceleratorProfileForType(_ context.Context, acceleratorType string) (*v1.AcceleratorProfile, bool, error) {
	if p.profileResolverErr != nil {
		return nil, false, p.profileResolverErr
	}

	profile, ok := p.acceleratorProfiles[acceleratorType]
	return profile, ok, nil
}

func (p *fakeStaticNodeAcceleratorPlugin) ValidateStaticClusterVersion(context.Context, string, string) (bool, error) {
	return p.validationMatched, p.validationErr
}
