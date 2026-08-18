package accelerator

import (
	"context"
	"errors"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
	"github.com/neutree-ai/neutree/internal/accelerator/resourceparser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManagerGetAcceleratorProfile(t *testing.T) {
	m := &manager{}
	m.acceleratorsMap.Store(v1.AcceleratorTypeNVIDIAGPU.String(), registerPlugin{
		resource: v1.AcceleratorTypeNVIDIAGPU.String(),
		plugin:   &plugin.GPUAcceleratorPlugin{},
	})

	profile, err := m.GetAcceleratorProfile(context.Background(), v1.AcceleratorTypeNVIDIAGPU.String())

	require.NoError(t, err)
	require.NotNil(t, profile)
	assert.Equal(t, v1.AcceleratorTypeNVIDIAGPU.String(), profile.AcceleratorType)
	require.NotNil(t, profile.MetricsExporter)
	assert.Equal(t, "dcgm-exporter", profile.MetricsExporter.Name)
}

func TestManagerGetEngineContainerRunOptionsNVIDIA(t *testing.T) {
	m := &manager{}
	m.acceleratorsMap.Store(v1.AcceleratorTypeNVIDIAGPU.String(), registerPlugin{
		resource: v1.AcceleratorTypeNVIDIAGPU.String(),
		plugin:   &plugin.GPUAcceleratorPlugin{},
	})

	opts, err := m.GetEngineContainerRunOptions(v1.AcceleratorTypeNVIDIAGPU.String())

	require.NoError(t, err)
	assert.Contains(t, opts, "--runtime=nvidia")
	assert.Contains(t, opts, "--gpus all")
}

func TestManagerGetEngineContainerRunOptionsEmptyForNoAccelerator(t *testing.T) {
	m := &manager{}

	opts, err := m.GetEngineContainerRunOptions("")

	assert.NoError(t, err)
	assert.Nil(t, opts)
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
				resource: tt.plugin.Resource(),
				plugin:   tt.plugin,
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

func TestManagerGetStaticNodeRuntimeConfigRequiresOwningPlugin(t *testing.T) {
	fallback := &fakeStaticNodeAcceleratorPlugin{
		resourceSet:          true,
		resource:             "fallback_gpu",
		staticRuntimeConfig:  &v1.RuntimeConfig{Env: map[string]string{"FALLBACK": "true"}},
		staticRuntimeMatched: true,
	}
	m := &manager{}
	m.acceleratorsMap.Store(fallback.Resource(), registerPlugin{
		resource: fallback.Resource(),
		plugin:   fallback,
	})

	config, err := m.GetStaticNodeRuntimeConfig(context.Background(), &v1.StaticNodeAcceleratorStatus{Type: "legacy_gpu"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "accelerator plugin legacy_gpu not found")
	assert.Nil(t, config)
	assert.Zero(t, fallback.staticRuntimeCalls)
}

func TestManagerGetStaticNodeRuntimeConfigDoesNotUseFallbackWhenOwnerHasNoResolver(t *testing.T) {
	fallback := &fakeStaticNodeAcceleratorPlugin{
		resourceSet:          true,
		resource:             "fallback_gpu",
		staticRuntimeConfig:  &v1.RuntimeConfig{Env: map[string]string{"FALLBACK": "true"}},
		staticRuntimeMatched: true,
	}
	m := &manager{}
	m.acceleratorsMap.Store("owner_gpu", registerPlugin{
		resource: "owner_gpu",
		plugin:   &plugin.GPUAcceleratorPlugin{},
	})
	m.acceleratorsMap.Store(fallback.Resource(), registerPlugin{
		resource: fallback.Resource(),
		plugin:   fallback,
	})

	config, err := m.GetStaticNodeRuntimeConfig(context.Background(), &v1.StaticNodeAcceleratorStatus{Type: "owner_gpu"})

	require.NoError(t, err)
	assert.Nil(t, config)
	assert.Zero(t, fallback.staticRuntimeCalls)
}

func TestManagerGetStaticNodeRuntimeConfigDoesNotCallUnrelatedResolver(t *testing.T) {
	unrelated := &fakeStaticNodeAcceleratorPlugin{
		resourceSet:          true,
		resource:             "unrelated_gpu",
		staticRuntimeConfig:  &v1.RuntimeConfig{Env: map[string]string{"UNRELATED": "true"}},
		staticRuntimeMatched: true,
	}
	m := &manager{}
	m.acceleratorsMap.Store("owner_gpu", registerPlugin{
		resource: "owner_gpu",
		plugin:   &plugin.GPUAcceleratorPlugin{},
	})
	m.acceleratorsMap.Store(unrelated.Resource(), registerPlugin{
		resource: unrelated.Resource(),
		plugin:   unrelated,
	})

	config, err := m.GetStaticNodeRuntimeConfig(context.Background(), &v1.StaticNodeAcceleratorStatus{Type: "owner_gpu"})

	require.NoError(t, err)
	assert.Nil(t, config)
	assert.Zero(t, unrelated.staticRuntimeCalls)
}

func TestManagerGetStaticNodeRuntimeConfigRejectsNilOwnerConfig(t *testing.T) {
	owner := &fakeStaticNodeAcceleratorPlugin{
		resourceSet:          true,
		resource:             "owner_gpu",
		staticRuntimeMatched: true,
	}
	m := &manager{}
	m.acceleratorsMap.Store(owner.Resource(), registerPlugin{
		resource: owner.Resource(),
		plugin:   owner,
	})

	config, err := m.GetStaticNodeRuntimeConfig(context.Background(), &v1.StaticNodeAcceleratorStatus{Type: "owner_gpu"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "owner_gpu returned nil config for a matched status")
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
	m.acceleratorsMap.Store(owner.Resource(), registerPlugin{resource: owner.Resource(), plugin: owner})
	m.acceleratorsMap.Store(unrelated.Resource(), registerPlugin{resource: unrelated.Resource(), plugin: unrelated})

	config, err := m.GetStaticNodeRuntimeConfig(context.Background(), &v1.StaticNodeAcceleratorStatus{Type: owner.Resource()})

	require.NoError(t, err)
	require.NotNil(t, config)
	assert.Equal(t, "true", config.Env["OWNER"])
}

func TestNewManagerWithPluginsRegistersInjectedPlugin(t *testing.T) {
	injected := &fakeStaticNodeAcceleratorPlugin{}

	manager, err := NewManagerWithPlugins(injected)

	require.NoError(t, err)
	assert.Contains(t, manager.SupportPlugins(), injected.Resource())
	assert.Contains(t, manager.SupportPlugins(), v1.AcceleratorTypeNVIDIAGPU.String())
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
			_, err := NewManagerWithPlugins(tt.plugins...)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.message)
		})
	}
}

func TestManagerAddInternalPluginsRegistersPluginsOnExistingManager(t *testing.T) {
	manager, err := NewManagerWithPlugins()
	require.NoError(t, err)

	injected := &fakeStaticNodeAcceleratorPlugin{resourceSet: true, resource: "injected_gpu"}

	err = manager.AddInternalPlugins(injected)

	require.NoError(t, err)
	assert.Contains(t, manager.SupportPlugins(), "injected_gpu")
	assert.Contains(t, manager.SupportPlugins(), v1.AcceleratorTypeNVIDIAGPU.String())
}

func TestManagerAddInternalPluginsFailsAtomically(t *testing.T) {
	valid := &fakeStaticNodeAcceleratorPlugin{resourceSet: true, resource: "injected_gpu"}
	emptyResource := &fakeStaticNodeAcceleratorPlugin{resourceSet: true}
	var typedNil *fakeStaticNodeAcceleratorPlugin

	tests := []struct {
		name    string
		plugins []plugin.AcceleratorPlugin
		message string
	}{
		{name: "batch with duplicate of manager-registered resource", plugins: []plugin.AcceleratorPlugin{valid, &fakeStaticNodeAcceleratorPlugin{resourceSet: true, resource: v1.AcceleratorTypeNVIDIAGPU.String()}}, message: "already registered"},
		{name: "batch with duplicate resource within batch", plugins: []plugin.AcceleratorPlugin{valid, valid}, message: "already registered"},
		{name: "batch with empty resource", plugins: []plugin.AcceleratorPlugin{valid, emptyResource}, message: "resource is required"},
		{name: "batch with typed nil", plugins: []plugin.AcceleratorPlugin{valid, typedNil}, message: "accelerator plugin is nil"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager, err := NewManagerWithPlugins()
			require.NoError(t, err)

			err = manager.AddInternalPlugins(tt.plugins...)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.message)
			assert.NotContains(t, manager.SupportPlugins(), "injected_gpu")
		})
	}
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
			Type:    "custom_gpu",
			Devices: []v1.StaticNodeAcceleratorDeviceStatus{{ID: "0", ProductModel: "custom-model"}},
		},
	}}
	m := &manager{}
	m.acceleratorsMap.Store("custom_gpu", registerPlugin{
		resource: "custom_gpu",
		plugin:   detector,
	})

	status, err := m.DetectAccelerator(context.Background(), "10.0.0.10", v1.Auth{
		SSHUser:       "root",
		SSHPrivateKey: "key",
	})

	require.NoError(t, err)
	require.NotNil(t, status)
	assert.Equal(t, "custom_gpu", status.Type)
	require.Len(t, status.Devices, 1)
	assert.Equal(t, "custom-model", status.Devices[0].ProductModel)
	assert.Zero(t, detector.getCalls)
}

func TestManagerDetectAcceleratorFallsBackToCPUWhenDetectorDoesNotMatch(t *testing.T) {
	detector := &fakeStaticNodeAcceleratorPlugin{}
	m := &manager{}
	m.acceleratorsMap.Store("custom_gpu", registerPlugin{
		resource: "custom_gpu",
		plugin:   detector,
	})

	status, err := m.DetectAccelerator(context.Background(), "10.0.0.10", v1.Auth{
		SSHUser:       "root",
		SSHPrivateKey: "key",
	})

	require.NoError(t, err)
	require.NotNil(t, status)
	assert.Equal(t, v1.StaticNodeAcceleratorTypeCPU, status.Type)
	assert.Zero(t, detector.getCalls)
}

func TestManagerDetectAcceleratorReturnsDetectorErrorWhenNothingMatches(t *testing.T) {
	detector := &fakeStaticNodeAcceleratorPlugin{staticDetectErr: errors.New("lspci unavailable")}
	m := &manager{}
	m.acceleratorsMap.Store("custom_gpu", registerPlugin{
		resource: "custom_gpu",
		plugin:   detector,
	})

	status, err := m.DetectAccelerator(context.Background(), "10.0.0.10", v1.Auth{
		SSHUser:       "root",
		SSHPrivateKey: "key",
	})

	require.Error(t, err)
	require.Nil(t, status)
	assert.Contains(t, err.Error(), "detect static node accelerator from plugin custom_gpu failed")
	assert.Zero(t, detector.getCalls)
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
	staticDetectErr      error
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
	if p.staticDetectErr != nil {
		return nil, p.staticDetectErr
	}

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
