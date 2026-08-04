package client

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClustersUpgradePreflight(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/api/v1/clusters/upgrade_preflight", r.URL.Path)
		assert.Equal(t, "default", r.URL.Query().Get("workspace"))
		assert.Equal(t, "cluster-a", r.URL.Query().Get("name"))
		assert.Equal(t, "v1.2.0", r.URL.Query().Get("target_version"))
		assert.Equal(t, "test-api-key", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"allowed":true,"source_version":"v1.1.0","target_version":"v1.2.0","upgrade_to":["v1.1.1","v1.2.0"],"release_info":{"baseline":"v1.2.0","revision":"revision-2"}}`))
	}))
	defer server.Close()

	apiClient := NewClient(server.URL, WithAPIKey("test-api-key"))
	result, err := apiClient.Clusters.UpgradePreflight("default", "cluster-a", "v1.2.0")

	require.NoError(t, err)
	assert.True(t, result.Allowed)
	assert.Equal(t, "v1.1.0", result.SourceVersion)
	assert.Equal(t, []string{"v1.1.1", "v1.2.0"}, result.UpgradeTo)
	assert.Equal(t, "revision-2", result.ReleaseInfo.Revision)
}

func TestClustersUpgradePreflightReturnsAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"not allowed"}`))
	}))
	defer server.Close()

	apiClient := NewClient(server.URL)
	_, err := apiClient.Clusters.UpgradePreflight("default", "cluster-a", "v1.2.0")

	require.Error(t, err)
	assert.True(t, HasStatus(err, http.StatusBadRequest))
}
