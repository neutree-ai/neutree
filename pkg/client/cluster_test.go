package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestClusterProfileUpsert(t *testing.T) {
	profile := &v1.ClusterProfile{
		APIVersion: "v1",
		Kind:       v1.ClusterProfileKind,
		Metadata:   &v1.Metadata{Name: "v1.2.0-alpha.1"},
		Spec: &v1.ClusterProfileSpec{Components: map[string]v1.ClusterProfileComponents{
			v1.SSHClusterType:        {},
			v1.KubernetesClusterType: {},
		}},
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, http.MethodPost, request.Method)
		assert.Equal(t, "/api/v1/clusters/profile_upsert", request.URL.Path)
		assert.Equal(t, "test-api-key", request.Header.Get("Authorization"))
		assert.Equal(t, "application/json", request.Header.Get("Content-Type"))

		var payload struct {
			Profile *v1.ClusterProfile `json:"profile"`
		}
		require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
		require.NotNil(t, payload.Profile)
		assert.Equal(t, profile.GetName(), payload.Profile.GetName())
		assert.Equal(t, profile.Spec.Components, payload.Profile.Spec.Components)
		_, err := writer.Write([]byte(`{"operation":"created"}`))
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)

	result, err := NewClient(server.URL, WithAPIKey("test-api-key")).Clusters.UpsertClusterProfile(profile)

	require.NoError(t, err)
	assert.Equal(t, "created", result.Operation)
}
