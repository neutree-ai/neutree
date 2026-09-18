package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kong/go-kong/kong"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.openly.dev/pointy"
)

func TestKongInitEnablesMetricsIdempotently(t *testing.T) {
	plugins := map[string]kong.Plugin{}
	creates := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			plugin, ok := plugins[strings.TrimPrefix(r.URL.Path, "/plugins/")]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				assert.NoError(t, json.NewEncoder(w).Encode(map[string]string{"message": "Not found"}))
				return
			}
			assert.NoError(t, json.NewEncoder(w).Encode(plugin))
			return
		}
		if !assert.Equal(t, http.MethodPost, r.Method) {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var plugin kong.Plugin
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&plugin)) || !assert.NotNil(t, plugin.InstanceName) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		plugin.ID = plugin.InstanceName
		plugins[*plugin.InstanceName] = plugin
		creates++
		assert.NoError(t, json.NewEncoder(w).Encode(plugin))
	}))
	defer server.Close()
	client, err := kong.NewClient(pointy.String(server.URL), server.Client())
	require.NoError(t, err)
	gateway := &Kong{kongClient: client, logRemoteWriteUrl: "http://vector:30122"}
	require.NoError(t, gateway.Init())
	require.NoError(t, gateway.Init())
	plugin, ok := plugins["neutree-prometheus"]
	require.True(t, ok, "initialization must enable the exporter without a manual Admin API call")
	assert.Equal(t, "prometheus", *plugin.Name)
	assert.Nil(t, plugin.Route)
	assert.Nil(t, plugin.Service)
	assert.Equal(t, len(plugins), creates, "reinitialization must not create duplicate plugins")
}
