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
				Metrics: &v1.AcceleratorMetricsProfile{
					Exporter: &v1.AcceleratorExporterProfile{
						Kind: "dcgm-exporter",
						Port: 9400,
					},
				},
			},
		})
		require.NoError(t, err)
	}))
	defer server.Close()

	client := newAcceleratorPluginClient(server.URL)
	provider, ok := client.(AcceleratorProfileProvider)
	require.True(t, ok)

	profile, err := provider.GetAcceleratorProfile(context.Background())

	require.NoError(t, err)
	require.NotNil(t, profile)
	assert.Equal(t, v1.AcceleratorTypeNVIDIAGPU.String(), profile.AcceleratorType)
	require.NotNil(t, profile.Metrics)
	require.NotNil(t, profile.Metrics.Exporter)
	assert.Equal(t, "dcgm-exporter", profile.Metrics.Exporter.Kind)
	assert.Equal(t, 9400, profile.Metrics.Exporter.Port)
}

func TestAcceleratorPluginClientGetAcceleratorProfileNotFound(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	client := newAcceleratorPluginClient(server.URL)
	provider, ok := client.(AcceleratorProfileProvider)
	require.True(t, ok)

	_, err := provider.GetAcceleratorProfile(context.Background())

	require.Error(t, err)
	assert.True(t, IsHTTPStatus(err, http.StatusNotFound))
}
