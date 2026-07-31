package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kong/go-kong/kong"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.openly.dev/pointy"

	v1 "github.com/neutree-ai/neutree/api/v1"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func externalUpstream(url string, models map[string]string) v1.ExternalEndpointUpstreamEntry {
	return v1.ExternalEndpointUpstreamEntry{
		Upstream:     &v1.ExternalEndpointUpstreamSpec{URL: url},
		Auth:         &v1.ExternalEndpointAuthSpec{Type: v1.ExternalEndpointAuthTypeBearer, Credential: "sk-test"},
		ModelMapping: models,
	}
}

func endpointRefUpstream(name string, models map[string]string) v1.ExternalEndpointUpstreamEntry {
	return v1.ExternalEndpointUpstreamEntry{
		EndpointRef:  pointy.String(name),
		ModelMapping: models,
	}
}

func testExternalEndpoint(entries ...v1.ExternalEndpointUpstreamEntry) *v1.ExternalEndpoint {
	return &v1.ExternalEndpoint{
		ID:       7,
		Metadata: &v1.Metadata{Name: "ee-1", Workspace: "ws-1"},
		Spec:     &v1.ExternalEndpointSpec{Upstreams: entries},
	}
}

func TestResolveExternalEndpointUpstreams(t *testing.T) {
	t.Run("external entries resolve without touching storage", func(t *testing.T) {
		k := &Kong{}
		ee := testExternalEndpoint(
			externalUpstream("https://api.openai.com/v1", map[string]string{"fast": "gpt-4o-mini"}),
		)

		resolved := k.resolveExternalEndpointUpstreams(ee)
		require.Len(t, resolved, 1)
		require.NoError(t, resolved[0].err)
		assert.Equal(t, "https", resolved[0].scheme)
		assert.Equal(t, "api.openai.com", resolved[0].host)
		assert.Equal(t, "/v1", resolved[0].path)
		assert.False(t, resolved[0].internal)
	})

	t.Run("a malformed url fails only its own entry", func(t *testing.T) {
		k := &Kong{}
		ee := testExternalEndpoint(
			externalUpstream("://not-a-url", map[string]string{"broken": "m"}),
			externalUpstream("https://api.openai.com/v1", map[string]string{"fast": "gpt-4o-mini"}),
		)

		resolved := k.resolveExternalEndpointUpstreams(ee)
		require.Len(t, resolved, 2)
		assert.Error(t, resolved[0].err)
		assert.NoError(t, resolved[1].err)
	})

	t.Run("an entry with neither ref nor upstream fails only itself", func(t *testing.T) {
		k := &Kong{}
		ee := testExternalEndpoint(
			v1.ExternalEndpointUpstreamEntry{ModelMapping: map[string]string{"orphan": "m"}},
			externalUpstream("https://api.openai.com/v1", map[string]string{"fast": "gpt-4o-mini"}),
		)

		resolved := k.resolveExternalEndpointUpstreams(ee)
		require.Len(t, resolved, 2)
		assert.ErrorContains(t, resolved[0].err, "neither endpoint_ref nor upstream")
		assert.NoError(t, resolved[1].err)
	})
}

func TestExternalEndpointUpstreamStatuses(t *testing.T) {
	resolved := []resolvedUpstream{
		{
			entry: endpointRefUpstream("ep-a", map[string]string{"b-model": "m2", "a-model": "m1"}),
		},
		{
			entry: externalUpstream("https://api.openai.com/v1", map[string]string{"fast": "gpt-4o-mini"}),
			err:   errors.New("boom"),
		},
	}

	statuses := externalEndpointUpstreamStatuses(resolved)
	require.Len(t, statuses, 2)

	assert.Equal(t, v1.ExternalEndpointUpstreamKindEndpointRef, statuses[0].Kind)
	assert.Equal(t, "ep-a", statuses[0].Ref)
	assert.Equal(t, v1.ExternalEndpointUpstreamPhaseReady, statuses[0].Phase)
	// exposed model names are sorted so the status does not churn between reconciles
	assert.Equal(t, []string{"a-model", "b-model"}, statuses[0].Models)
	assert.Empty(t, statuses[0].ErrorMessage)

	assert.Equal(t, v1.ExternalEndpointUpstreamKindExternal, statuses[1].Kind)
	// the URL identifies the entry; the credential must never reach the status
	assert.Equal(t, "https://api.openai.com/v1", statuses[1].Ref)
	assert.Equal(t, v1.ExternalEndpointUpstreamPhaseFailed, statuses[1].Phase)
	assert.Equal(t, "boom", statuses[1].ErrorMessage)
	assert.NotContains(t, statuses[1].ErrorMessage, "sk-test")
}

func TestGenerateExternalEndpointAIGatewayPluginSkipsUnresolved(t *testing.T) {
	k := &Kong{}
	ee := testExternalEndpoint(
		endpointRefUpstream("ep-gone", map[string]string{"gone": "m"}),
		externalUpstream("https://api.openai.com/v1", map[string]string{"fast": "gpt-4o-mini"}),
	)
	route := &kong.Route{ID: pointy.String("route-1")}

	// only the healthy entry is handed to the generator
	ready := []resolvedUpstream{{
		entry:  ee.Spec.Upstreams[1],
		scheme: "https",
		host:   "api.openai.com",
		port:   443,
		path:   "/v1",
	}}

	plugin := k.generateExternalEndpointAIGatewayPlugin(ee, route, ready)
	require.NotNil(t, plugin)

	upstreams, ok := plugin.Config["upstreams"].([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, upstreams, 1, "the unresolved endpoint_ref must be left out of the pushed config")
	assert.Equal(t, "api.openai.com", upstreams[0]["host"])
	assert.Equal(t, false, upstreams[0]["internal"])
	assert.Equal(t, "Bearer sk-test", upstreams[0]["auth_header"])
}

func TestGenerateExternalEndpointAIGatewayPluginInternalEntry(t *testing.T) {
	k := &Kong{}
	entry := endpointRefUpstream("ep-a", map[string]string{"m": "m"})
	// an internal ref must never carry an auth header even if one was stored
	entry.Auth = &v1.ExternalEndpointAuthSpec{Type: v1.ExternalEndpointAuthTypeBearer, Credential: "sk-leak"}
	ee := testExternalEndpoint(entry)

	plugin := k.generateExternalEndpointAIGatewayPlugin(ee, &kong.Route{ID: pointy.String("r")},
		[]resolvedUpstream{{entry: entry, scheme: "http", host: "10.0.0.1", port: 8000, path: "/ws-1/ep-a", internal: true}})

	upstreams, ok := plugin.Config["upstreams"].([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, upstreams, 1)
	assert.Equal(t, true, upstreams[0]["internal"])
	assert.Nil(t, upstreams[0]["auth_header"])
}

// fakeKongForExternalEndpoint serves the minimum admin API surface
// SyncExternalEndpoint touches, and records the upstream list of the pushed
// ai-gateway plugin.
func fakeKongForExternalEndpoint(t *testing.T, pushed *[]interface{}) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/services/"):
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not found"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/services":
			// Echo the created service back so the reconcile sees no drift and
			// does not follow up with a PATCH.
			var svc kong.Service
			require.NoError(t, json.NewDecoder(r.Body).Decode(&svc))
			svc.ID = pointy.String("svc-1")
			require.NoError(t, json.NewEncoder(w).Encode(svc))
		case r.Method == http.MethodGet && r.URL.Path == "/routes/route-1/plugins":
			_, _ = w.Write([]byte(`{"data":[]}`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/routes/"):
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not found"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/routes":
			var route kong.Route
			require.NoError(t, json.NewDecoder(r.Body).Decode(&route))
			route.ID = pointy.String("route-1")
			require.NoError(t, json.NewEncoder(w).Encode(route))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/plugins/"):
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not found"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/plugins":
			var body kong.Plugin
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))

			if body.Name != nil && *body.Name == "neutree-ai-gateway" {
				ups, ok := body.Config["upstreams"].([]interface{})
				require.True(t, ok)
				*pushed = ups
			}

			_, _ = w.Write([]byte(`{"id":"plugin-1"}`))
		default:
			t.Fatalf("unexpected Kong request: %s %s", r.Method, r.URL.Path)
		}
	}))
}

// TestSyncExternalEndpointPartialUpstreamFailure is the NEU-580 case: an
// endpoint_ref whose internal endpoint was deleted must not stop the remaining
// upstreams from being pushed to Kong.
func TestSyncExternalEndpointPartialUpstreamFailure(t *testing.T) {
	var pushed []interface{}

	server := fakeKongForExternalEndpoint(t, &pushed)
	defer server.Close()

	client, err := kong.NewClient(pointy.String(server.URL), server.Client())
	require.NoError(t, err)

	s := storagemocks.NewMockStorage(t)
	// the referenced internal endpoint no longer exists
	s.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{}, nil)

	k := &Kong{kongClient: client, storage: s}
	ee := testExternalEndpoint(
		// a broken entry *first*, which used to abort the whole sync
		endpointRefUpstream("ep-deleted", map[string]string{"gone": "m"}),
		externalUpstream("https://api.openai.com/v1", map[string]string{"fast": "gpt-4o-mini"}),
	)

	statuses, err := k.SyncExternalEndpoint(ee)
	require.NoError(t, err, "a single broken upstream must not fail the sync")

	require.Len(t, pushed, 1, "the healthy upstream is still pushed to Kong")
	healthy, ok := pushed[0].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "api.openai.com", healthy["host"])

	require.Len(t, statuses, 2)
	assert.Equal(t, v1.ExternalEndpointUpstreamPhaseFailed, statuses[0].Phase)
	assert.Equal(t, "ep-deleted", statuses[0].Ref)
	assert.Contains(t, statuses[0].ErrorMessage, "not found")
	assert.Equal(t, []string{"gone"}, statuses[0].Models)
	assert.Equal(t, v1.ExternalEndpointUpstreamPhaseReady, statuses[1].Phase)
}

// TestSyncExternalEndpointAllUpstreamsFailed keeps the hard-failure contract:
// with nothing left to route to, the sync fails and the caller marks the
// endpoint Failed rather than Degraded.
func TestSyncExternalEndpointAllUpstreamsFailed(t *testing.T) {
	s := storagemocks.NewMockStorage(t)
	s.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{}, nil)

	k := &Kong{storage: s}
	ee := testExternalEndpoint(endpointRefUpstream("ep-deleted", map[string]string{"gone": "m"}))

	statuses, err := k.SyncExternalEndpoint(ee)
	require.Error(t, err)
	assert.ErrorContains(t, err, "no resolvable upstream")
	// the per-upstream detail is still returned so the status explains why
	require.Len(t, statuses, 1)
	assert.Equal(t, v1.ExternalEndpointUpstreamPhaseFailed, statuses[0].Phase)
}

// An endpoint with no upstreams at all is a spec problem, and must keep saying
// so rather than reporting "nothing resolved" with an empty list of reasons.
func TestSyncExternalEndpointNoUpstreamsConfigured(t *testing.T) {
	k := &Kong{}

	statuses, err := k.SyncExternalEndpoint(testExternalEndpoint())
	require.Error(t, err)
	assert.ErrorContains(t, err, "has no upstreams configured")
	assert.Empty(t, statuses)
}

func TestJoinUpstreamErrors(t *testing.T) {
	statuses := []v1.ExternalEndpointUpstreamStatus{
		{Phase: v1.ExternalEndpointUpstreamPhaseFailed, ErrorMessage: "first failed"},
		{Phase: v1.ExternalEndpointUpstreamPhaseReady},
		{Phase: v1.ExternalEndpointUpstreamPhaseFailed, ErrorMessage: "second failed"},
	}

	assert.Equal(t, "first failed; second failed", joinUpstreamErrors(statuses))
	assert.Empty(t, joinUpstreamErrors([]v1.ExternalEndpointUpstreamStatus{
		{Phase: v1.ExternalEndpointUpstreamPhaseReady},
	}))
}
