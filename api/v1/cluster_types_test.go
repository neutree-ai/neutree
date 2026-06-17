package v1

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClusterAcceleratorVirtualizationSerialization(t *testing.T) {
	cluster := &Cluster{
		Spec: &ClusterSpec{
			Type: KubernetesClusterType,
			AcceleratorVirtualization: &AcceleratorVirtualizationSpec{
				Enabled: true,
				ConfigPatch: map[string]interface{}{
					"devicePlugin": map[string]interface{}{
						"nvidiaDriverRoot": "/run/nvidia/driver",
					},
				},
			},
		},
		Status: &ClusterStatus{
			ComponentStatus: map[string]*ComponentStatus{
				ComponentStatusAcceleratorVirtualizationKey: {
					Phase:   ComponentPhaseReady,
					Managed: true,
					Version: "v2.9.0",
				},
			},
		},
	}

	data, err := json.Marshal(cluster)
	require.NoError(t, err)

	var got Cluster
	require.NoError(t, json.Unmarshal(data, &got))

	require.NotNil(t, got.Spec.AcceleratorVirtualization)
	assert.True(t, got.Spec.AcceleratorVirtualization.Enabled)
	assert.True(t, got.Spec.AcceleratorVirtualizationEnabled())
	assert.Equal(t, "/run/nvidia/driver", got.Spec.AcceleratorVirtualization.ConfigPatch["devicePlugin"].(map[string]interface{})["nvidiaDriverRoot"])

	require.NotNil(t, got.Status.ComponentStatus[ComponentStatusAcceleratorVirtualizationKey])
	assert.Equal(t, ComponentPhaseReady, got.Status.ComponentStatus[ComponentStatusAcceleratorVirtualizationKey].Phase)
	assert.Equal(t, "v2.9.0", got.Status.ComponentStatus[ComponentStatusAcceleratorVirtualizationKey].Version)
}

func TestClusterAcceleratorVirtualizationDisabledWhenMissing(t *testing.T) {
	spec := &ClusterSpec{}
	assert.False(t, spec.AcceleratorVirtualizationEnabled())

	spec.AcceleratorVirtualization = &AcceleratorVirtualizationSpec{}
	assert.False(t, spec.AcceleratorVirtualizationEnabled())
}
