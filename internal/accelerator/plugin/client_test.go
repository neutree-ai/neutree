package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAcceleratorPluginClientGetAcceleratorProfile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, v1.GetAcceleratorProfilePath, r.URL.Path)

		err := json.NewEncoder(w).Encode(v1.GetAcceleratorProfileResponse{
			Profile: v1.AcceleratorProfile{
				AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.String(),
				ClusterRuntime: &v1.RuntimeConfig{
					Runtime: "nvidia",
				},
			},
		})
		require.NoError(t, err)
	}))
	defer server.Close()

	client := newAcceleratorPluginClient(server.URL)

	profile, err := client.GetAcceleratorProfile(context.Background())

	require.NoError(t, err)
	require.NotNil(t, profile)
	assert.Equal(t, v1.AcceleratorTypeNVIDIAGPU.String(), profile.AcceleratorType)
	require.NotNil(t, profile.ClusterRuntime)
	assert.Equal(t, "nvidia", profile.ClusterRuntime.Runtime)
}

func TestAcceleratorPluginClientGetAcceleratorProfileNotFound(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	client := newAcceleratorPluginClient(server.URL)

	_, err := client.GetAcceleratorProfile(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "status code: 404")
}

func TestAcceleratorPluginClientDetectStaticNodeAccelerator(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, v1.DetectStaticNodeAcceleratorPath, r.URL.Path)

		request := &v1.DetectStaticNodeAcceleratorRequest{}
		require.NoError(t, json.NewDecoder(r.Body).Decode(request))
		assert.Equal(t, "10.0.0.10", request.NodeIp)
		assert.Equal(t, "root", request.SSHAuth.SSHUser)

		err := json.NewEncoder(w).Encode(v1.DetectStaticNodeAcceleratorResponse{
			Matched: true,
			Accelerator: &v1.StaticNodeAcceleratorStatus{
				Type: v1.AcceleratorTypeNVIDIAGPU.String(),
			},
		})
		require.NoError(t, err)
	}))
	defer server.Close()

	client := newAcceleratorPluginClient(server.URL)

	response, err := client.DetectStaticNodeAccelerator(context.Background(), &v1.DetectStaticNodeAcceleratorRequest{
		NodeIp: "10.0.0.10",
		SSHAuth: v1.Auth{
			SSHUser:       "root",
			SSHPrivateKey: "key",
		},
	})

	require.NoError(t, err)
	require.NotNil(t, response)
	assert.True(t, response.Matched)
	require.NotNil(t, response.Accelerator)
	assert.Equal(t, v1.AcceleratorTypeNVIDIAGPU.String(), response.Accelerator.Type)
}

func TestAcceleratorPluginClientDetectStaticNodeAcceleratorNotFoundReturnsError(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	client := newAcceleratorPluginClient(server.URL)

	response, err := client.DetectStaticNodeAccelerator(context.Background(), &v1.DetectStaticNodeAcceleratorRequest{
		NodeIp: "10.0.0.10",
	})

	require.Error(t, err)
	assert.Nil(t, response)
	assert.Contains(t, err.Error(), "failed to detect static node accelerator from accelerator plugin")
	assert.Contains(t, err.Error(), "status code: 404")
}
