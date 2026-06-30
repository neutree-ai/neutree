package accelerator

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

	profile, supported, err := m.GetAcceleratorProfile(context.Background(), v1.AcceleratorTypeNVIDIAGPU.String())

	require.NoError(t, err)
	assert.True(t, supported)
	require.NotNil(t, profile)
	assert.Equal(t, v1.AcceleratorTypeNVIDIAGPU.String(), profile.AcceleratorType)
	require.NotNil(t, profile.ClusterRuntime)
	assert.Equal(t, "nvidia", profile.ClusterRuntime.Runtime)
}

func TestManagerGetAcceleratorProfileFromExternalPlugin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, v1.GetAcceleratorProfilePath, r.URL.Path)

		err := json.NewEncoder(w).Encode(v1.GetAcceleratorProfileResponse{
			Profile: v1.AcceleratorProfile{
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

	profile, supported, err := m.GetAcceleratorProfile(context.Background(), "external_gpu")

	require.NoError(t, err)
	assert.True(t, supported)
	require.NotNil(t, profile)
	assert.Equal(t, "external_gpu", profile.AcceleratorType)
	require.NotNil(t, profile.ClusterRuntime)
	assert.Equal(t, "custom-cluster", profile.ClusterRuntime.Runtime)
	require.NotNil(t, profile.EngineRuntime)
	assert.Equal(t, "custom-engine", profile.EngineRuntime.Runtime)
}

func TestManagerGetAcceleratorProfileRejectsMismatchedProfileType(t *testing.T) {
	provider := &fakeStaticNodeAcceleratorPlugin{
		acceleratorProfile: &v1.AcceleratorProfile{AcceleratorType: "other_gpu"},
	}
	m := &manager{}
	m.acceleratorsMap.Store("custom_gpu", registerPlugin{
		resource:         "custom_gpu",
		plugin:           provider,
		lastRegisterTime: time.Now(),
	})

	profile, supported, err := m.GetAcceleratorProfile(context.Background(), "custom_gpu")

	require.Error(t, err)
	assert.False(t, supported)
	assert.Nil(t, profile)
	assert.Contains(t, err.Error(), "profile accelerator type other_gpu does not match requested type custom_gpu")
}

func TestManagerGetEngineRuntimeConfigUsesProfileEngineRuntime(t *testing.T) {
	expected := &v1.RuntimeConfig{
		ImageSuffix: "cuda-engine",
		Runtime:     "nvidia",
		Options:     []string{"--gpus", "all"},
	}
	provider := &fakeStaticNodeAcceleratorPlugin{
		acceleratorProfile: &v1.AcceleratorProfile{
			AcceleratorType: "custom_gpu",
			EngineRuntime:   expected,
		},
	}
	m := &manager{}
	m.acceleratorsMap.Store("custom_gpu", registerPlugin{
		resource:         "custom_gpu",
		plugin:           provider,
		lastRegisterTime: time.Now(),
	})

	runtimeConfig, supported, err := m.GetEngineRuntimeConfig(context.Background(), "custom_gpu")

	require.NoError(t, err)
	assert.True(t, supported)
	assert.Equal(t, *expected, runtimeConfig)
}

func TestManagerGetEngineRuntimeConfigNilEngineRuntimeIsEmptyConfig(t *testing.T) {
	provider := &fakeStaticNodeAcceleratorPlugin{
		acceleratorProfile: &v1.AcceleratorProfile{AcceleratorType: "custom_gpu"},
	}
	m := &manager{}
	m.acceleratorsMap.Store("custom_gpu", registerPlugin{
		resource:         "custom_gpu",
		plugin:           provider,
		lastRegisterTime: time.Now(),
	})

	runtimeConfig, supported, err := m.GetEngineRuntimeConfig(context.Background(), "custom_gpu")

	require.NoError(t, err)
	assert.True(t, supported)
	assert.Equal(t, v1.RuntimeConfig{}, runtimeConfig)
}

func TestManagerGetAcceleratorProfileNotFoundIsUnsupported(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	m := &manager{}
	m.acceleratorsMap.Store("external_gpu", registerPlugin{
		resource:         "external_gpu",
		plugin:           plugin.NewAcceleratorRestPlugin("external_gpu", server.URL),
		lastRegisterTime: time.Now(),
	})

	profile, supported, err := m.GetAcceleratorProfile(context.Background(), "external_gpu")

	require.NoError(t, err)
	assert.False(t, supported)
	assert.Nil(t, profile)
}

func TestManagerGetAcceleratorProfileMissingPlugin(t *testing.T) {
	m := &manager{}

	profile, supported, err := m.GetAcceleratorProfile(context.Background(), "missing")

	require.Error(t, err)
	assert.False(t, supported)
	assert.Nil(t, profile)
	assert.Contains(t, err.Error(), "accelerator plugin missing not found")
}

func TestManagerDetectAcceleratorDelegatesToPluginDetector(t *testing.T) {
	expected := &v1.StaticNodeAcceleratorStatus{
		Type:         "custom_gpu",
		Vendor:       "custom",
		ProductName:  "Custom GPU",
		ProductModel: "custom-gpu",
	}
	detector := &fakeStaticNodeAcceleratorPlugin{detected: expected, matched: true}
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
	require.Equal(t, expected, status)
	assert.Empty(t, status.Devices, "type discovery must not report detailed device data")
	assert.Equal(t, 1, detector.detectCalls)
	assert.Equal(t, "10.0.0.10", detector.detectRequest.NodeIp)
	assert.Equal(t, "root", detector.detectRequest.SSHAuth.SSHUser)
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
	assert.Equal(t, 1, detector.detectCalls)
}

func TestManagerDetectAcceleratorTreatsDetectorErrorAsCPUFallback(t *testing.T) {
	detector := &fakeStaticNodeAcceleratorPlugin{detectErr: errors.New("lspci unavailable")}
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
	assert.Equal(t, 1, detector.detectCalls)
}

type fakeStaticNodeAcceleratorPlugin struct {
	detected *v1.StaticNodeAcceleratorStatus
	matched  bool

	detectErr     error
	detectCalls   int
	detectRequest *v1.DetectStaticNodeAcceleratorRequest

	acceleratorProfile *v1.AcceleratorProfile
}

func (p *fakeStaticNodeAcceleratorPlugin) Resource() string {
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
	p.detectCalls++
	p.detectRequest = request

	return &v1.DetectStaticNodeAcceleratorResponse{
		Accelerator: p.detected,
		Matched:     p.matched,
	}, p.detectErr
}

func (p *fakeStaticNodeAcceleratorPlugin) GetNodeAccelerator(
	ctx context.Context,
	request *v1.GetNodeAcceleratorRequest,
) (*v1.GetNodeAcceleratorResponse, error) {
	return nil, nil
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
