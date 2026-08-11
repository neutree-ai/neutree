package accelerator_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/accelerator"
)

type testClusterVirtualizationConfigProvider struct{}

func (testClusterVirtualizationConfigProvider) ResolveClusterVirtualizationConfig(
	context.Context,
	*v1.Cluster,
) (*accelerator.VirtualizationConfig, error) {
	return &accelerator.VirtualizationConfig{
		Supported:       true,
		BlockingReasons: []string{"example blocking reason"},
		CandidateNodes:  []string{"worker-0"},
		NodeScopeLabel: accelerator.VirtualizationNodeScopeLabel{
			Key:           "example.com/virtualization-enabled",
			EnabledValue:  "enabled",
			DisabledValue: "disabled",
		},
		DevicePluginTemplate: &accelerator.DevicePluginTemplate{
			Manifest: "apiVersion: apps/v1\nkind: DaemonSet\n",
		},
		ConfigPatch: map[string]interface{}{
			"devices": map[string]interface{}{
				"example": true,
			},
		},
	}, nil
}

func TestClusterVirtualizationConfigProviderContract(t *testing.T) {
	var provider accelerator.ClusterVirtualizationConfigProvider = testClusterVirtualizationConfigProvider{}

	config, err := provider.ResolveClusterVirtualizationConfig(context.Background(), &v1.Cluster{})

	require.NoError(t, err)
	require.NotNil(t, config)
	assert.True(t, config.Supported)
	assert.Equal(t, []string{"example blocking reason"}, config.BlockingReasons)
	assert.Equal(t, []string{"worker-0"}, config.CandidateNodes)
	assert.Equal(t, "example.com/virtualization-enabled", config.NodeScopeLabel.Key)
	assert.Equal(t, "enabled", config.NodeScopeLabel.EnabledValue)
	assert.Equal(t, "disabled", config.NodeScopeLabel.DisabledValue)
	require.NotNil(t, config.DevicePluginTemplate)
	assert.Equal(t, "apiVersion: apps/v1\nkind: DaemonSet\n", config.DevicePluginTemplate.Manifest)
	devicesPatch, ok := config.ConfigPatch["devices"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, true, devicesPatch["example"])
}

func TestResolveEffectiveMode(t *testing.T) {
	core := v1.AcceleratorVirtualizationModeCore
	template := v1.AcceleratorVirtualizationModeTemplate

	tests := []struct {
		name      string
		requested v1.AcceleratorVirtualizationMode
		def       v1.AcceleratorVirtualizationMode
		supported []v1.AcceleratorVirtualizationMode
		wantMode  v1.AcceleratorVirtualizationMode
		wantOK    bool
	}{
		{name: "empty requested uses default", def: template, supported: []v1.AcceleratorVirtualizationMode{template, core}, wantMode: template, wantOK: true},
		{name: "requested supported", requested: core, def: template, supported: []v1.AcceleratorVirtualizationMode{template, core}, wantMode: core, wantOK: true},
		{name: "requested unsupported", requested: template, def: core, supported: []v1.AcceleratorVirtualizationMode{core}, wantMode: template, wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mode, ok := accelerator.ResolveEffectiveMode(tt.requested, tt.def, tt.supported)
			assert.Equal(t, tt.wantMode, mode)
			assert.Equal(t, tt.wantOK, ok)
		})
	}
}
