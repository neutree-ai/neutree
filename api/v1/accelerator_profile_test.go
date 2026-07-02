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
			Runtime:     "nvidia",
			Env: map[string]string{
				"ACCELERATOR_TYPE": "gpu",
			},
			Options: []string{"--gpus all"},
		},
		EngineRuntime: &RuntimeConfig{
			ImageSuffix: "cuda-engine",
			Runtime:     "nvidia",
			Options:     []string{"--gpus", "all"},
		},
	}

	data, err := json.Marshal(profile)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"cluster_runtime"`)
	assert.Contains(t, string(data), `"engine_runtime"`)

	decoded := AcceleratorProfile{}
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, string(AcceleratorTypeNVIDIAGPU), decoded.AcceleratorType)
	require.NotNil(t, decoded.ClusterRuntime)
	assert.Equal(t, "cuda", decoded.ClusterRuntime.ImageSuffix)
	assert.Equal(t, "nvidia", decoded.ClusterRuntime.Runtime)
	assert.Equal(t, []string{"--gpus all"}, decoded.ClusterRuntime.Options)
	require.NotNil(t, decoded.EngineRuntime)
	assert.Equal(t, "cuda-engine", decoded.EngineRuntime.ImageSuffix)
	assert.Equal(t, "nvidia", decoded.EngineRuntime.Runtime)
	assert.Equal(t, []string{"--gpus", "all"}, decoded.EngineRuntime.Options)
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
