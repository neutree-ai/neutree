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

func TestClusterReleaseCompatibilitySerialization(t *testing.T) {
	cluster := &Cluster{Status: &ClusterStatus{
		ReleaseInfo: &ReleaseInfoReference{Baseline: "v1.2.0", Revision: "revision-2"},
		ReleaseCompatibility: &ClusterReleaseCompatibility{
			EffectiveVersion: "v1.2.0",
			ResolvedVersion:  "v1.2.0",
			State:            ClusterReleaseCompatibilityStateRetired,
			Reason:           "cluster version v1.2.0 is retired",
		},
	}}

	data, err := json.Marshal(cluster)
	require.NoError(t, err)

	var got Cluster
	require.NoError(t, json.Unmarshal(data, &got))
	require.NotNil(t, got.Status.ReleaseInfo)
	assert.Equal(t, "revision-2", got.Status.ReleaseInfo.Revision)
	require.NotNil(t, got.Status.ReleaseCompatibility)
	assert.Equal(t, ClusterReleaseCompatibilityStateRetired, got.Status.ReleaseCompatibility.State)
	assert.Equal(t, "cluster version v1.2.0 is retired", got.Status.ReleaseCompatibility.Reason)
}
