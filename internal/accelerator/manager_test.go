package accelerator

import (
	"context"
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
	require.NotNil(t, profile.Metrics)
	require.NotNil(t, profile.Metrics.Exporter)
	assert.Equal(t, "dcgm-exporter", profile.Metrics.Exporter.Kind)
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
		Devices: []v1.StaticNodeAcceleratorDeviceStatus{
			{ID: "0", ProductName: "Custom GPU", Healthy: true},
		},
	}
	detector := &fakeStaticNodeAcceleratorPlugin{detected: expected, matched: true}
	m := &manager{}
	m.acceleratorsMap.Store("custom_gpu", registerPlugin{
		resource:         "custom_gpu",
		plugin:           detector,
		lastRegisterTime: time.Now(),
	})

	status, err := m.DetectAccelerator(context.Background(), fakeNodeCommandRunner{})

	require.NoError(t, err)
	require.Equal(t, expected, status)
	assert.Equal(t, 1, detector.detectCalls)
}

func TestManagerDetectAcceleratorFallsBackToCPUWhenDetectorDoesNotMatch(t *testing.T) {
	detector := &fakeStaticNodeAcceleratorPlugin{}
	m := &manager{}
	m.acceleratorsMap.Store("custom_gpu", registerPlugin{
		resource:         "custom_gpu",
		plugin:           detector,
		lastRegisterTime: time.Now(),
	})

	status, err := m.DetectAccelerator(context.Background(), fakeNodeCommandRunner{})

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

	status, err := m.DetectAccelerator(context.Background(), fakeNodeCommandRunner{})

	require.NoError(t, err)
	require.NotNil(t, status)
	assert.Equal(t, v1.StaticNodeAcceleratorTypeCPU, status.Type)
	assert.Equal(t, 1, detector.detectCalls)
}

func TestManagerRuntimeProfileDelegatesToPluginProvider(t *testing.T) {
	expected := &v1.AcceleratorProfile{AcceleratorType: "custom_gpu"}
	provider := &fakeStaticNodeAcceleratorPlugin{
		runtimeProfile:      expected,
		runtimeProfileFound: true,
	}
	m := &manager{}
	m.acceleratorsMap.Store("custom_gpu", registerPlugin{
		resource:         "custom_gpu",
		plugin:           provider,
		lastRegisterTime: time.Now(),
	})
	accelerator := v1.StaticNodeAcceleratorStatus{
		Type:         "custom_gpu",
		ProductModel: "custom-gpu-special",
	}

	profile, supported, err := m.RuntimeProfile(context.Background(), accelerator)

	require.NoError(t, err)
	assert.True(t, supported)
	require.Equal(t, expected, profile)
	assert.Equal(t, 1, provider.runtimeProfileCalls)
	assert.Equal(t, accelerator, provider.runtimeProfileAccelerator)
}

func TestManagerRuntimeProfileCPUIsUnsupported(t *testing.T) {
	m := &manager{}

	profile, supported, err := m.RuntimeProfile(context.Background(), v1.CPUStaticNodeAcceleratorStatus())

	require.NoError(t, err)
	assert.False(t, supported)
	assert.Nil(t, profile)
}

type fakeNodeCommandRunner struct{}

func (fakeNodeCommandRunner) Run(ctx context.Context, command string) (string, error) {
	return "", nil
}

type fakeStaticNodeAcceleratorPlugin struct {
	detected *v1.StaticNodeAcceleratorStatus
	matched  bool

	detectErr   error
	detectCalls int

	runtimeProfile            *v1.AcceleratorProfile
	runtimeProfileFound       bool
	runtimeProfileAccelerator v1.StaticNodeAcceleratorStatus
	runtimeProfileCalls       int
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
	runner plugin.NodeCommandRunner,
) (*v1.StaticNodeAcceleratorStatus, bool, error) {
	p.detectCalls++

	return p.detected, p.matched, p.detectErr
}

func (p *fakeStaticNodeAcceleratorPlugin) RuntimeProfile(
	ctx context.Context,
	accelerator v1.StaticNodeAcceleratorStatus,
) (*v1.AcceleratorProfile, bool, error) {
	p.runtimeProfileCalls++
	p.runtimeProfileAccelerator = accelerator

	return p.runtimeProfile, p.runtimeProfileFound, nil
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
