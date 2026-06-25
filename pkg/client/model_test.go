package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestFinalizePushPostsImportedMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/api/v1/workspaces/default/model_registries/registry/models/demo/finalize", r.URL.Path)
		require.Equal(t, "api-key", r.Header.Get("Authorization"))

		var body map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "v1", body["version"])
		require.Equal(t, "2026-06-25T00:00:00Z", body["creation_time"])
		require.Equal(t, "1MB", body["size"])

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	c := NewClient(server.URL, WithAPIKey("api-key"))
	err := c.Models.FinalizePush("default", "registry", "demo", &v1.ModelVersion{
		Name:         "v1",
		CreationTime: "2026-06-25T00:00:00Z",
		Size:         "1MB",
	})
	require.NoError(t, err)
}
