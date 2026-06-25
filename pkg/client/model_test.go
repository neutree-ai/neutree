package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestFinalizePushPostsVersionAndDecodesModelVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/api/v1/workspaces/default/model_registries/registry/models/demo/finalize", r.URL.Path)
		require.Equal(t, "api-key", r.Header.Get("Authorization"))

		var body map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "v1", body["version"])

		require.NoError(t, json.NewEncoder(w).Encode(&v1.ModelVersion{Name: "v1", Size: "1MB"}))
	}))
	defer server.Close()

	c := NewClient(server.URL, WithAPIKey("api-key"))
	version, err := c.Models.FinalizePush("default", "registry", "demo", "v1")
	require.NoError(t, err)
	require.Equal(t, "v1", version.Name)
}
