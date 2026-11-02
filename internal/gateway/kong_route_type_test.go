package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kong/go-kong/kong"
	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/utils"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// NEU-633: an endpoint whose engine ships its own model (e.g. Flex) may have
// a nil Spec.Model. getEndpointRouteType must fall back to the default route
// type instead of dereferencing it.
func TestGetEndpointRouteType_NilModel(t *testing.T) {
	ep := &v1.Endpoint{
		Spec: &v1.EndpointSpec{
			Engine: &v1.EndpointEngineSpec{Engine: "flex", Version: "1.0.0"},
		},
	}

	assert.Equal(t, v1.RouteTypeChatCompletions, getEndpointRouteType(ep))
}

func TestGetEndpointRouteType_ByTask(t *testing.T) {
	tests := []struct {
		task     string
		expected string
	}{
		{v1.TextGenerationModelTask, v1.RouteTypeChatCompletions},
		{v1.TextEmbeddingModelTask, v1.RouteTypeEmbeddings},
		{v1.TextRerankModelTask, v1.RouteTypeRerank},
		{"", v1.RouteTypeChatCompletions},
	}

	for _, tt := range tests {
		ep := &v1.Endpoint{
			Spec: &v1.EndpointSpec{
				Model: &v1.ModelSpec{Task: tt.task},
			},
		}

		assert.Equal(t, tt.expected, getEndpointRouteType(ep))
	}
}

func TestEndpointDirectAddressesAgree(t *testing.T) {
	cases := []struct {
		name      string
		kind      string
		dashboard string
		host      string
		port      int
		wantURL   string
	}{
		{name: "Kubernetes router", kind: v1.KubernetesClusterType, dashboard: "http://router.example:30932", host: "router.example", port: 30932,
			wantURL: "http://router.example:30932/team%2Fa/model%3Fname"},
		{name: "SSH IPv6 serve port", kind: v1.SSHClusterType, dashboard: "http://[2001:db8::1]:8265", host: "2001:db8::1", port: 8000,
			wantURL: "http://[2001:db8::1]:8000/team%2Fa/model%3Fname"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			endpoint := &v1.Endpoint{Metadata: &v1.Metadata{Workspace: "team/a", Name: "model?name"}, Spec: &v1.EndpointSpec{Cluster: "cluster"}}
			cluster := v1.Cluster{Spec: &v1.ClusterSpec{Type: tc.kind}, Status: &v1.ClusterStatus{DashboardURL: tc.dashboard}}
			store := &storagemocks.MockStorage{}
			store.On("ListCluster", mock.Anything).Return([]v1.Cluster{cluster}, nil)
			store.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{*endpoint}, nil)
			var pushed kong.Service
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet {
					w.WriteHeader(http.StatusNotFound)
					_, _ = w.Write([]byte(`{"message":"Not found"}`))
					return
				}
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, "/services", r.URL.Path)
				require.NoError(t, json.NewDecoder(r.Body).Decode(&pushed))
				w.WriteHeader(http.StatusCreated)
				require.NoError(t, json.NewEncoder(w).Encode(pushed))
			}))
			defer server.Close()
			client, err := kong.NewClient(&server.URL, server.Client())
			require.NoError(t, err)
			k := &Kong{storage: store, kongClient: client}

			_, err = k.syncEndpointService(endpoint)
			require.NoError(t, err)
			assert.Equal(t, "http", *pushed.Protocol)
			assert.Equal(t, tc.host, *pushed.Host)
			assert.Equal(t, tc.port, *pushed.Port)
			assert.Equal(t, "/team%2Fa/model%3Fname", *pushed.Path)

			scheme, host, port, path, err := k.resolveEndpointRef("team/a", "model?name")
			require.NoError(t, err)
			assert.Equal(t, "http", scheme)
			assert.Equal(t, tc.host, host)
			assert.Equal(t, tc.port, port)
			assert.Equal(t, "/team%2Fa/model%3Fname", path)
			serveURL, err := utils.FormatServiceURL(&cluster, endpoint)
			require.NoError(t, err)
			assert.Equal(t, tc.wantURL, serveURL)
			store.AssertExpectations(t)
		})
	}
}
