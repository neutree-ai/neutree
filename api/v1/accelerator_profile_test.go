package v1

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAcceleratorProfileJSONRoundTrip(t *testing.T) {
	profile := AcceleratorProfile{
		AcceleratorType: string(AcceleratorTypeNVIDIAGPU),
		ClusterRuntime: &RuntimeConfig{
			ImageSuffix: "cuda",
		},
		EndpointRuntime: &RuntimeConfig{
			Runtime: "nvidia",
			Options: []string{"--gpus all"},
			Env: map[string]string{
				"ACCELERATOR_TYPE": "gpu",
			},
		},
		ResourceDefaults: &AcceleratorResourceDefaults{
			RayResourceName:        "GPU",
			KubernetesResourceName: "nvidia.com/gpu",
		},
	}

	data, err := json.Marshal(profile)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"ray_resource_name":"GPU"`)

	decoded := AcceleratorProfile{}
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, string(AcceleratorTypeNVIDIAGPU), decoded.AcceleratorType)
	require.NotNil(t, decoded.ClusterRuntime)
	assert.Equal(t, "cuda", decoded.ClusterRuntime.ImageSuffix)
	require.NotNil(t, decoded.EndpointRuntime)
	assert.Equal(t, "nvidia", decoded.EndpointRuntime.Runtime)
	require.NotNil(t, decoded.ResourceDefaults)
	assert.Equal(t, "nvidia.com/gpu", decoded.ResourceDefaults.KubernetesResourceName)
}

func TestGetAcceleratorProfileResponse(t *testing.T) {
	response := GetAcceleratorProfileResponse{
		Profile: AcceleratorProfile{
			AcceleratorType: string(AcceleratorTypeAMDGPU),
		},
	}

	data, err := json.Marshal(response)
	require.NoError(t, err)
	assert.JSONEq(t, `{"profile":{"accelerator_type":"amd_gpu"}}`, string(data))
}
