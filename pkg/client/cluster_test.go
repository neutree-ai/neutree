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
		Spec: &v1.ClusterProfileSpec{Components: map[string]v1.ClusterProfileComponents{
			v1.SSHClusterType:        {},
			v1.KubernetesClusterType: {},
		}},
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
		assert.Equal(t, profile.Spec.Components, request.Profile.Spec.Components)
		_, _ = w.Write([]byte(`{"operation":"created"}`))
	}))
	defer server.Close()

	result, err := NewClient(server.URL, WithAPIKey("test-api-key")).Clusters.UpsertClusterProfile(profile)

	require.NoError(t, err)
	assert.Equal(t, "created", result.Operation)
}
