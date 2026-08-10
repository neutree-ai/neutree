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
	profile := &v1.ClusterProfile{Metadata: &v1.Metadata{Name: "v1.2.0-alpha.1"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/v1/clusters/profile_upsert", r.URL.Path)
		assert.Equal(t, "test-api-key", r.Header.Get("Authorization"))

		var request struct {
			Profile     *v1.ClusterProfile `json:"profile"`
			ForceUpdate bool               `json:"force_update"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		assert.Equal(t, profile.GetName(), request.Profile.GetName())
		assert.True(t, request.ForceUpdate)
		_, _ = w.Write([]byte(`{"operation":"updated"}`))
	}))
	defer server.Close()

	result, err := NewClient(server.URL, WithAPIKey("test-api-key")).Clusters.UpsertClusterProfile(profile, true)

	require.NoError(t, err)
	assert.Equal(t, "updated", result.Operation)
}
