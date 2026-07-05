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

	profile, err := m.GetAcceleratorProfile(context.Background(), v1.AcceleratorTypeNVIDIAGPU.String())

	require.NoError(t, err)
	require.NotNil(t, profile)
	assert.Equal(t, v1.AcceleratorTypeNVIDIAGPU.String(), profile.AcceleratorType)
	require.NotNil(t, profile.MetricsExporter)
	assert.Equal(t, "dcgm-exporter", profile.MetricsExporter.Name)
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

func TestManagerDetectAcceleratorSupportsExternalPluginWithoutStaticDetectEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	accelerators []v1.Accelerator
	getErr       error
	getCalls     int
	getRequest   *v1.GetNodeAcceleratorRequest

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
