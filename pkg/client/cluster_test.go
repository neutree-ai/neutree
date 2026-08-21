package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClusterProfileUpsert(t *testing.T) {
	profile := &v1.ClusterProfile{
		Metadata: &v1.Metadata{Name: "v1.2.0-alpha.1"},
		Spec:     &v1.ClusterProfileSpec{ClusterType: v1.SSHClusterType},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/v1/clusters/profile_upsert", r.URL.Path)
		assert.Equal(t, "test-api-key", r.Header.Get("Authorization"))

		var request struct {
			Profile *v1.ClusterProfile `json:"profile"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		assert.Equal(t, profile.GetName(), request.Profile.GetName())
		assert.Equal(t, v1.SSHClusterType, request.Profile.GetClusterType())
		_, _ = w.Write([]byte(`{"operation":"updated"}`))
	}))
	defer server.Close()

	result, err := NewClient(server.URL, WithAPIKey("test-api-key")).Clusters.UpsertClusterProfile(profile)

	require.NoError(t, err)
	assert.Equal(t, "updated", result.Operation)
}

func TestListClusterProfileVersions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/api/v1/clusters/profile_versions", r.URL.Path)
		assert.Equal(t, "test-api-key", r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"profiles":[{"version":"v1.2.0","cluster_type":"kubernetes"},{"version":"v1.2.0","cluster_type":"ssh"}]}`))
	}))
	defer server.Close()

	profiles, err := NewClient(server.URL, WithAPIKey("test-api-key")).Clusters.ListClusterProfileVersions()

	require.NoError(t, err)
	assert.Equal(t, []ClusterProfileVersion{
		{Version: "v1.2.0", ClusterType: v1.KubernetesClusterType},
		{Version: "v1.2.0", ClusterType: v1.SSHClusterType},
	}, profiles)
}
