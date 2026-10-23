package client

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClientGetsClusterCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/api/v1/credentials/clusters", r.URL.Path)
		require.Equal(t, "eq.default", r.URL.Query().Get("metadata->>workspace"))
		require.Equal(t, "eq.cluster-a", r.URL.Query().Get("metadata->>name"))
		require.Equal(t, "test-api-key", r.Header.Get("Authorization"))
		_, err := w.Write([]byte(`[{"metadata":{"workspace":"default","name":"cluster-a"}}]`))
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)

	cluster, err := NewClient(server.URL, WithAPIKey("test-api-key")).GetClusterCredentials("default", "cluster-a")
	require.NoError(t, err)
	require.Equal(t, "default", cluster.Metadata.Workspace)
	require.Equal(t, "cluster-a", cluster.Metadata.Name)
}
