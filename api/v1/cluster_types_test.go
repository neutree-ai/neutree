package v1

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestRouterProxyConfigSerialization(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{name: "omitted values", body: `{}`, want: `{}`},
		{
			name: "null uses router defaults",
			body: `{"max_connections_per_backend":null,"connect_timeout_seconds":null,"sock_connect_timeout_seconds":null,"sock_read_timeout_seconds":null}`,
			want: `{}`,
		},
		{
			name: "explicit zero disables limits",
			body: `{"max_connections_per_backend":0,"connect_timeout_seconds":0,"sock_connect_timeout_seconds":0,"sock_read_timeout_seconds":0}`,
			want: `{"max_connections_per_backend":0,"connect_timeout_seconds":0,"sock_connect_timeout_seconds":0,"sock_read_timeout_seconds":0}`,
		},
		{
			name: "configured pool and fractional timeouts",
			body: `{"version":"v1.3.0","max_connections_per_backend":128,"connect_timeout_seconds":1.5,"sock_connect_timeout_seconds":0.25,"sock_read_timeout_seconds":600}`,
			want: `{"version":"v1.3.0","max_connections_per_backend":128,"connect_timeout_seconds":1.5,"sock_connect_timeout_seconds":0.25,"sock_read_timeout_seconds":600}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var router RouterSpec
			require.NoError(t, json.Unmarshal([]byte(tc.body), &router))
			encoded, err := json.Marshal(router)
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(encoded))

			yamlBody, err := yaml.Marshal(router)
			require.NoError(t, err)
			var yamlFields map[string]interface{}
			require.NoError(t, yaml.Unmarshal(yamlBody, &yamlFields))
			yamlJSON, err := json.Marshal(yamlFields)
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(yamlJSON))

			var decoded RouterSpec
			require.NoError(t, yaml.Unmarshal(yamlBody, &decoded))
			assert.Equal(t, router, decoded)
		})
	}
}

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
					Version: "v2.10.0",
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
	assert.Equal(t, "v2.10.0", got.Status.ComponentStatus[ComponentStatusAcceleratorVirtualizationKey].Version)
}

func TestClusterAcceleratorVirtualizationDisabledWhenMissing(t *testing.T) {
	spec := &ClusterSpec{}
	assert.False(t, spec.AcceleratorVirtualizationEnabled())

	spec.AcceleratorVirtualization = &AcceleratorVirtualizationSpec{}
	assert.False(t, spec.AcceleratorVirtualizationEnabled())
}

func TestAcceleratorVirtualizationModeConstants(t *testing.T) {
	assert.Equal(t, AcceleratorVirtualizationMode("template"), AcceleratorVirtualizationModeTemplate)
	assert.Equal(t, AcceleratorVirtualizationMode("core"), AcceleratorVirtualizationModeCore)
}

func TestClusterAcceleratorVirtualizationModeSerialization(t *testing.T) {
	cluster := &Cluster{
		Metadata: &Metadata{Name: "test", Workspace: "default"},
		Spec: &ClusterSpec{
			Type: KubernetesClusterType,
			AcceleratorVirtualization: &AcceleratorVirtualizationSpec{
				Enabled: true,
				Mode:    AcceleratorVirtualizationModeTemplate,
			},
		},
		Status: &ClusterStatus{
			AcceleratorVirtualization: &AcceleratorVirtualizationStatus{
				Mode:               AcceleratorVirtualizationModeTemplate,
				SupportedResources: []string{"virtualization.memory_mib"},
			},
		},
	}

	data, err := json.Marshal(cluster)
	require.NoError(t, err)

	var got Cluster
	require.NoError(t, json.Unmarshal(data, &got))
	require.NotNil(t, got.Spec.AcceleratorVirtualization)
	assert.Equal(t, AcceleratorVirtualizationModeTemplate, got.Spec.AcceleratorVirtualization.Mode)
	require.NotNil(t, got.Status.AcceleratorVirtualization)
	assert.Equal(t, AcceleratorVirtualizationModeTemplate, got.Status.AcceleratorVirtualization.Mode)
	assert.Equal(t, []string{"virtualization.memory_mib"}, got.Status.AcceleratorVirtualization.SupportedResources)
}
