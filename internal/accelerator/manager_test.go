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

func TestManagerGetStaticNodeRuntimeConfig(t *testing.T) {
	tests := []struct {
		name    string
		plugin  *fakeStaticNodeAcceleratorPlugin
		wantNil bool
		wantErr string
	}{
		{
			name: "matching resolver",
			plugin: &fakeStaticNodeAcceleratorPlugin{
				staticRuntimeConfig:  &v1.RuntimeConfig{Env: map[string]string{"VISIBLE_DEVICES": "0,1"}},
				staticRuntimeMatched: true,
			},
		},
		{
			name:    "owner resolver does not match",
			plugin:  &fakeStaticNodeAcceleratorPlugin{},
			wantNil: true,
			wantErr: "static runtime resolver for accelerator type custom_gpu from owner plugin custom_gpu did not match",
		},
		{
			name: "owner resolver returns nil config",
			plugin: &fakeStaticNodeAcceleratorPlugin{
				staticRuntimeMatched: true,
			},
			wantNil: true,
			wantErr: "static runtime resolver for accelerator type custom_gpu from plugin custom_gpu returned nil config for a matched status",
		},
		{
			name:    "resolver error",
			plugin:  &fakeStaticNodeAcceleratorPlugin{staticRuntimeErr: errors.New("unsupported static runtime")},
			wantNil: true,
			wantErr: "get static node runtime config for accelerator type custom_gpu from plugin custom_gpu",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &manager{}
			m.acceleratorsMap.Store(tt.plugin.Resource(), registerPlugin{
				resource:         tt.plugin.Resource(),
				plugin:           tt.plugin,
				lastRegisterTime: time.Now(),
			})

			config, err := m.GetStaticNodeRuntimeConfig(context.Background(), &v1.StaticNodeAcceleratorStatus{Type: "custom_gpu"})

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			if tt.wantNil {
				assert.Nil(t, config)
				return
			}
			require.NotNil(t, config)
			assert.Equal(t, "0,1", config.Env["VISIBLE_DEVICES"])
		})
	}
}

func TestManagerGetStaticNodeRuntimeConfigDoesNotUseFallbackWithoutOwningPlugin(t *testing.T) {
	fallback := &fakeStaticNodeAcceleratorPlugin{
		resourceSet:          true,
		resource:             "fallback_gpu",
		staticRuntimeConfig:  &v1.RuntimeConfig{Env: map[string]string{"FALLBACK": "true"}},
		staticRuntimeMatched: true,
	}
	m := &manager{}
	m.acceleratorsMap.Store(fallback.Resource(), registerPlugin{
		resource:         fallback.Resource(),
		plugin:           fallback,
		lastRegisterTime: time.Now(),
	})

	config, err := m.GetStaticNodeRuntimeConfig(context.Background(), &v1.StaticNodeAcceleratorStatus{Type: "legacy_gpu"})

	require.NoError(t, err)
	assert.Nil(t, config)
	assert.Zero(t, fallback.staticRuntimeCalls)
}

func TestManagerGetStaticNodeRuntimeConfigUsesFallbackWhenOwnerHasNoResolver(t *testing.T) {
	fallback := &fakeStaticNodeAcceleratorPlugin{
		resourceSet:          true,
		resource:             "fallback_gpu",
		staticRuntimeConfig:  &v1.RuntimeConfig{Env: map[string]string{"FALLBACK": "true"}},
		staticRuntimeMatched: true,
	}
	m := &manager{}
	m.acceleratorsMap.Store("owner_gpu", registerPlugin{
		resource:         "owner_gpu",
		plugin:           &plugin.GPUAcceleratorPlugin{},
		lastRegisterTime: time.Now(),
	})
	m.acceleratorsMap.Store(fallback.Resource(), registerPlugin{
		resource:         fallback.Resource(),
		plugin:           fallback,
		lastRegisterTime: time.Now(),
	})

	config, err := m.GetStaticNodeRuntimeConfig(context.Background(), &v1.StaticNodeAcceleratorStatus{Type: "owner_gpu"})

	require.NoError(t, err)
	require.NotNil(t, config)
	assert.Equal(t, "true", config.Env["FALLBACK"])
	assert.Equal(t, 1, fallback.staticRuntimeCalls)
}

func TestManagerGetStaticNodeRuntimeConfigStopsAtFirstFallbackError(t *testing.T) {
	failing := &fakeStaticNodeAcceleratorPlugin{
		resourceSet:      true,
		resource:         "a_fallback_gpu",
		staticRuntimeErr: errors.New("unsafe static runtime"),
	}
	laterMatch := &fakeStaticNodeAcceleratorPlugin{
		resourceSet:          true,
		resource:             "z_fallback_gpu",
		staticRuntimeConfig:  &v1.RuntimeConfig{Env: map[string]string{"LATER": "true"}},
		staticRuntimeMatched: true,
	}
	m := &manager{}
	m.acceleratorsMap.Store("owner_gpu", registerPlugin{
		resource:         "owner_gpu",
		plugin:           &plugin.GPUAcceleratorPlugin{},
		lastRegisterTime: time.Now(),
	})
	m.acceleratorsMap.Store(failing.Resource(), registerPlugin{
		resource:         failing.Resource(),
		plugin:           failing,
		lastRegisterTime: time.Now(),
	})
	m.acceleratorsMap.Store(laterMatch.Resource(), registerPlugin{
		resource:         laterMatch.Resource(),
		plugin:           laterMatch,
		lastRegisterTime: time.Now(),
	})

	config, err := m.GetStaticNodeRuntimeConfig(context.Background(), &v1.StaticNodeAcceleratorStatus{Type: "owner_gpu"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "a_fallback_gpu")
	assert.Nil(t, config)
}

func TestManagerGetStaticNodeRuntimeConfigRejectsNilFallbackConfig(t *testing.T) {
	fallback := &fakeStaticNodeAcceleratorPlugin{
		resourceSet:          true,
		resource:             "fallback_gpu",
		staticRuntimeMatched: true,
	}
	m := &manager{}
	m.acceleratorsMap.Store("owner_gpu", registerPlugin{
		resource:         "owner_gpu",
		plugin:           &plugin.GPUAcceleratorPlugin{},
		lastRegisterTime: time.Now(),
	})
	m.acceleratorsMap.Store(fallback.Resource(), registerPlugin{
		resource:         fallback.Resource(),
		plugin:           fallback,
		lastRegisterTime: time.Now(),
	})

	config, err := m.GetStaticNodeRuntimeConfig(context.Background(), &v1.StaticNodeAcceleratorStatus{Type: "owner_gpu"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "fallback_gpu returned nil config for a matched status")
	assert.Nil(t, config)
}

func TestManagerGetStaticNodeRuntimeConfigPrioritizesOwningPlugin(t *testing.T) {
	owner := &fakeStaticNodeAcceleratorPlugin{
		resourceSet:          true,
		resource:             "owner_gpu",
		staticRuntimeConfig:  &v1.RuntimeConfig{Env: map[string]string{"OWNER": "true"}},
		staticRuntimeMatched: true,
	}
	unrelated := &fakeStaticNodeAcceleratorPlugin{
		resourceSet:      true,
		resource:         "unrelated_gpu",
		staticRuntimeErr: errors.New("must not resolve unrelated accelerator"),
	}
	m := &manager{}
	m.acceleratorsMap.Store(owner.Resource(), registerPlugin{resource: owner.Resource(), plugin: owner, lastRegisterTime: time.Now()})
	m.acceleratorsMap.Store(unrelated.Resource(), registerPlugin{resource: unrelated.Resource(), plugin: unrelated, lastRegisterTime: time.Now()})

	config, err := m.GetStaticNodeRuntimeConfig(context.Background(), &v1.StaticNodeAcceleratorStatus{Type: owner.Resource()})

	require.NoError(t, err)
	require.NotNil(t, config)
	assert.Equal(t, "true", config.Env["OWNER"])
}

func TestNewManagerWithPluginsRegistersInjectedPlugin(t *testing.T) {
	injected := &fakeStaticNodeAcceleratorPlugin{}

	manager, err := NewManagerWithPlugins(gin.New(), injected)

	require.NoError(t, err)
	assert.Contains(t, manager.SupportPlugins(), injected.Resource())
	assert.Contains(t, manager.SupportPlugins(), v1.AcceleratorTypeNVIDIAGPU.String())
}

func TestNewManagerWithPluginsRequiresGinEngine(t *testing.T) {
	_, err := NewManagerWithPlugins(nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "gin engine is required")
}

func TestNewManagerWithPluginsRejectsInvalidPlugins(t *testing.T) {
	var typedNil *fakeStaticNodeAcceleratorPlugin
	emptyResource := &fakeStaticNodeAcceleratorPlugin{resourceSet: true}
	injected := &fakeStaticNodeAcceleratorPlugin{}

	tests := []struct {
		name    string
		plugins []plugin.AcceleratorPlugin
		message string
	}{
		{name: "nil plugin", plugins: []plugin.AcceleratorPlugin{nil}, message: "accelerator plugin is nil"},
		{name: "typed nil plugin", plugins: []plugin.AcceleratorPlugin{typedNil}, message: "accelerator plugin is nil"},
		{name: "empty resource", plugins: []plugin.AcceleratorPlugin{emptyResource}, message: "resource is required"},
		{name: "duplicate resource", plugins: []plugin.AcceleratorPlugin{injected, injected}, message: "already registered"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewManagerWithPlugins(gin.New(), tt.plugins...)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.message)
		})
	}
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

func TestManagerDetectAcceleratorPreservesStaticNodeAcceleratorStatus(t *testing.T) {
	detector := &fakeStaticNodeAcceleratorPlugin{staticResponse: &v1.DetectStaticNodeAcceleratorResponse{
		Matched: true,
		Accelerator: &v1.StaticNodeAcceleratorStatus{
			Type:    "npu",
			Devices: []v1.StaticNodeAcceleratorDeviceStatus{{ID: "0", ProductModel: "HUAWEI_Ascend910B"}},
		},
	}}
	m := &manager{}
	m.acceleratorsMap.Store("npu", registerPlugin{
		resource:         "npu",
		plugin:           detector,
		lastRegisterTime: time.Now(),
	})

	status, err := m.DetectAccelerator(context.Background(), "10.0.0.10", v1.Auth{
		SSHUser:       "root",
		SSHPrivateKey: "key",
	})

	require.NoError(t, err)
	require.NotNil(t, status)
	assert.Equal(t, "npu", status.Type)
	require.Len(t, status.Devices, 1)
	assert.Equal(t, "HUAWEI_Ascend910B", status.Devices[0].ProductModel)
	assert.Zero(t, detector.getCalls)
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
	resourceSet bool
	resource    string

	accelerators []v1.Accelerator
	getErr       error
	getCalls     int
	getRequest   *v1.GetNodeAcceleratorRequest

	acceleratorProfile *v1.AcceleratorProfile

	staticRuntimeConfig  *v1.RuntimeConfig
	staticRuntimeMatched bool
	staticRuntimeErr     error
	staticRuntimeCalls   int
	staticResponse       *v1.DetectStaticNodeAcceleratorResponse
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

func (p *fakeStaticNodeAcceleratorPlugin) GetStaticNodeRuntimeConfig(context.Context, *v1.StaticNodeAcceleratorStatus) (*v1.RuntimeConfig, bool, error) {
	p.staticRuntimeCalls++

	return p.staticRuntimeConfig, p.staticRuntimeMatched, p.staticRuntimeErr
}
