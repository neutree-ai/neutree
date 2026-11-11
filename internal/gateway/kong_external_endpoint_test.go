package gateway

import (
	"encoding/json"
	"fmt"
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

	statuses := externalEndpointUpstreamStatuses(testExternalEndpoint(), resolved)
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

	plugin := k.generateExternalEndpointAIGatewayPlugin(ee, route, ready, nil)
	require.NotNil(t, plugin)

	upstreams, ok := plugin.Config["upstreams"].([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, upstreams, 1, "the unresolved endpoint_ref must be left out of the pushed config")
	assert.Equal(t, "api.openai.com", upstreams[0]["host"])
	assert.NotContains(t, upstreams[0], "name")
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
		[]resolvedUpstream{{entry: entry, scheme: "http", host: "10.0.0.1", port: 8000, path: "/ws-1/ep-a", internal: true}}, nil)

	upstreams, ok := plugin.Config["upstreams"].([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, upstreams, 1)
	assert.Equal(t, true, upstreams[0]["internal"])
	assert.Nil(t, upstreams[0]["auth_header"])
	assert.NotContains(t, upstreams[0], "name")
}

func TestCompileExternalEndpointModelRoutes(t *testing.T) {
	ee := testExternalEndpoint(
		externalUpstream("https://api.openai.com/v1", map[string]string{}),
	)
	ee.Spec.Upstreams[0].Name = "openai"
	ee.Spec.ModelRoutes = []v1.ExternalEndpointModelRoute{{
		Model:               "company-chat",
		RetryableConditions: []string{"http_429", "timeout"},
		MaxAttempts:         1,
		Targets: []v1.ExternalEndpointModelRouteTarget{{
			Upstream: "openai", UpstreamModel: "gpt-4o", Priority: 0, Weight: 100,
		}},
	}}

	routes, err := compileExternalEndpointModelRoutes(ee, []resolvedUpstream{{
		entry: ee.Spec.Upstreams[0], scheme: "https", host: "api.openai.com", port: 443, path: "/v1",
	}})
	require.NoError(t, err)
	require.Len(t, routes, 1)
	assert.Equal(t, "company-chat", routes[0]["model"])
	assert.Equal(t, []string{"http_429", "timeout"}, routes[0]["retryable_conditions"])
	assert.Equal(t, 1, routes[0]["max_attempts"])
	targets := routes[0]["targets"].([]map[string]interface{})
	require.Len(t, targets, 1)
	assert.Equal(t, "gpt-4o", targets[0]["upstream_model"])
	assert.NotContains(t, targets[0], "scheme")
	assert.NotContains(t, targets[0], "host")
	assert.NotContains(t, targets[0], "port")
	assert.NotContains(t, targets[0], "path")
	assert.NotContains(t, targets[0], "internal")
	assert.NotContains(t, targets[0], "auth_header")
}

func TestCompileExternalEndpointModelRoutesRejectsUnknownProvider(t *testing.T) {
	ee := testExternalEndpoint(externalUpstream("https://api.openai.com/v1", nil))
	ee.Spec.Upstreams[0].Name = "openai"
	ee.Spec.ModelRoutes = []v1.ExternalEndpointModelRoute{{
		Model:   "company-chat",
		Targets: []v1.ExternalEndpointModelRouteTarget{{Upstream: "missing", UpstreamModel: "gpt-4o"}},
	}}

	_, err := compileExternalEndpointModelRoutes(ee, []resolvedUpstream{{entry: ee.Spec.Upstreams[0]}})
	assert.ErrorContains(t, err, `references unknown upstream "missing"`)
}

func TestCompileExternalEndpointModelRoutesSkipsFailedProvider(t *testing.T) {
	healthy := externalUpstream("https://healthy.example/v1", nil)
	healthy.Name = "healthy"
	failed := externalUpstream("https://failed.example/v1", nil)
	failed.Name = "failed"
	ee := testExternalEndpoint(healthy, failed)
	ee.Spec.ModelRoutes = []v1.ExternalEndpointModelRoute{{
		Model: "company-chat",
		Targets: []v1.ExternalEndpointModelRouteTarget{
			{Upstream: "healthy", UpstreamModel: "gpt-4o"},
			{Upstream: "failed", UpstreamModel: "gpt-4o"},
		},
	}}

	routes, err := compileExternalEndpointModelRoutes(ee, []resolvedUpstream{
		{entry: healthy, scheme: "https", host: "healthy.example", port: 443, path: "/v1"},
		{entry: failed, err: errors.New("provider unavailable")},
	})
	require.NoError(t, err)
	require.Len(t, routes, 1)
	targets := routes[0]["targets"].([]map[string]interface{})
	require.Len(t, targets, 1)
	assert.Equal(t, "healthy", targets[0]["upstream"])
}

func TestGenerateExternalEndpointAIGatewayPluginPreservesEmptyModelRoutes(t *testing.T) {
	ee := testExternalEndpoint(externalUpstream("https://failed.example/v1", nil))
	ee.Spec.Upstreams[0].Name = "failed"
	ee.Spec.ModelRoutes = []v1.ExternalEndpointModelRoute{{
		Model: "company-chat",
		Targets: []v1.ExternalEndpointModelRouteTarget{{
			Upstream: "failed", UpstreamModel: "gpt-4o",
		}},
	}}

	plugin := (&Kong{}).generateExternalEndpointAIGatewayPlugin(
		ee,
		&kong.Route{ID: pointy.String("r")},
		[]resolvedUpstream{{entry: ee.Spec.Upstreams[0]}},
		[]map[string]interface{}{},
	)

	assert.Contains(t, plugin.Config, "model_routes")
	assert.Empty(t, plugin.Config["model_routes"])
}

// fakeKongForExternalEndpoint serves the minimum admin API surface
// SyncExternalEndpoint touches, and records the upstream list of the pushed
// ai-gateway plugin.
func fakeKongForExternalEndpoint(t *testing.T, pushed *kong.Configuration) *httptest.Server {
	t.Helper()
	plugins := make(map[string]kong.Plugin)

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
			if plugin, ok := plugins[strings.TrimPrefix(r.URL.Path, "/plugins/")]; ok {
				require.NoError(t, json.NewEncoder(w).Encode(plugin))
			} else {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"message":"Not found"}`))
			}
		case r.Method == http.MethodPost && r.URL.Path == "/plugins", r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/plugins/"):
			var body kong.Plugin
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))

			if body.Name != nil && *body.Name == "neutree-ai-gateway" {
				*pushed = body.Config
			}

			body.ID = body.InstanceName
			plugins[*body.InstanceName] = body
			require.NoError(t, json.NewEncoder(w).Encode(body))
		default:
			t.Fatalf("unexpected Kong request: %s %s", r.Method, r.URL.Path)
		}
	}))
}

// TestSyncExternalEndpointPartialUpstreamFailure is the NEU-580 case: an
// endpoint_ref whose internal endpoint was deleted must not stop the remaining
// upstreams from being pushed to Kong.
func TestSyncExternalEndpointPartialUpstreamFailure(t *testing.T) {
	var pushed kong.Configuration

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

	upstreams := pushed["upstreams"].([]interface{})
	require.Len(t, upstreams, 1, "the healthy upstream is still pushed to Kong")
	healthy, ok := upstreams[0].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "api.openai.com", healthy["host"])

	require.Len(t, statuses, 2)
	assert.Equal(t, v1.ExternalEndpointUpstreamPhaseFailed, statuses[0].Phase)
	assert.Equal(t, "ep-deleted", statuses[0].Ref)
	assert.Contains(t, statuses[0].ErrorMessage, "not found")
	assert.Equal(t, []string{"gone"}, statuses[0].Models)
	assert.Equal(t, v1.ExternalEndpointUpstreamPhaseReady, statuses[1].Phase)
}

// Exercise both accepted config formats through create, failover, total outage
// and recovery. The fake Admin API retains plugin state to exercise PATCH as
// well as initial creation, including replacement with empty arrays.
func TestSyncExternalEndpointAvailabilityLifecycle(t *testing.T) {
	for _, modelRoutes := range []bool{false, true} {
		t.Run(fmt.Sprintf("model_routes=%v", modelRoutes), func(t *testing.T) {
			var pushed kong.Configuration
			server := fakeKongForExternalEndpoint(t, &pushed)
			defer server.Close()
			client, err := kong.NewClient(pointy.String(server.URL), server.Client())
			require.NoError(t, err)
			primary := v1.Endpoint{Metadata: &v1.Metadata{Name: "primary"}, Spec: &v1.EndpointSpec{Cluster: "c"}}
			backup := v1.Endpoint{Metadata: &v1.Metadata{Name: "backup"}, Spec: &v1.EndpointSpec{Cluster: "c"}}
			s := storagemocks.NewMockStorage(t)
			s.On("ListCluster", mock.Anything).Return([]v1.Cluster{{
				Spec:   &v1.ClusterSpec{Type: v1.KubernetesClusterType},
				Status: &v1.ClusterStatus{Phase: v1.ClusterPhaseUpdating, DashboardURL: "http://router:8000"},
			}}, nil)
			k := &Kong{kongClient: client, storage: s}
			ee := testExternalEndpoint(endpointRefUpstream("primary", map[string]string{"chat": "a"}),
				endpointRefUpstream("backup", map[string]string{"chat": "b"}))
			ee.Spec.Upstreams[0].Name, ee.Spec.Upstreams[1].Name = "primary", "backup"
			if modelRoutes {
				ee.Spec.ModelRoutes = []v1.ExternalEndpointModelRoute{{Model: "chat", Targets: []v1.ExternalEndpointModelRouteTarget{
					{Upstream: "primary", UpstreamModel: "a"}, {Upstream: "backup", UpstreamModel: "b", Priority: 1},
				}}}
			}
			for _, step := range []struct {
				primary, backup *v1.EndpointStatus
				models          []string
			}{
				{&v1.EndpointStatus{Phase: v1.EndpointPhaseDEPLOYING}, &v1.EndpointStatus{Phase: v1.EndpointPhaseDEPLOYING}, []string{}},
				{&v1.EndpointStatus{Phase: v1.EndpointPhaseRUNNING}, &v1.EndpointStatus{Phase: v1.EndpointPhaseRUNNING}, []string{"a", "b"}},
				{&v1.EndpointStatus{Phase: v1.EndpointPhaseDEPLOYING, Resources: &v1.EndpointResourceStatus{ReadyReplicas: 1}, ErrorMessage: "Endpoint deploying in progress: Deployment: 1/2 replicas ready"}, &v1.EndpointStatus{Phase: v1.EndpointPhaseRUNNING}, []string{"a", "b"}},
				{&v1.EndpointStatus{Phase: v1.EndpointPhaseFAILED}, &v1.EndpointStatus{Phase: v1.EndpointPhaseRUNNING}, []string{"b"}},
				{&v1.EndpointStatus{Phase: v1.EndpointPhaseFAILED}, &v1.EndpointStatus{Phase: v1.EndpointPhasePAUSED}, []string{}},
				{&v1.EndpointStatus{Phase: v1.EndpointPhaseRUNNING}, &v1.EndpointStatus{Phase: v1.EndpointPhasePAUSED}, []string{"a"}},
			} {
				primary.Status, backup.Status = step.primary, step.backup
				s.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{primary}, nil).Once()
				s.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{backup}, nil).Once()
				statuses, err := k.SyncExternalEndpoint(ee)
				require.NoError(t, err)
				require.Len(t, statuses, 2)
				assert.Len(t, pushed["upstreams"], len(step.models))
				routes := pushed["model_routes"].([]interface{})
				require.Len(t, routes, 1)
				route := routes[0].(map[string]interface{})
				assert.Equal(t, "chat", route["model"])
				models := []string{}
				for _, target := range route["targets"].([]interface{}) {
					models = append(models, target.(map[string]interface{})["upstream_model"].(string))
				}
				assert.Equal(t, step.models, models)
			}
		})
	}
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

func TestResolveEndpointRefAvailability(t *testing.T) {
	for _, tc := range []struct {
		name            string
		replicas        *int
		status          *v1.EndpointStatus
		endpointDeleted bool
		wantOK          bool
		wantErr         string
	}{
		{name: "no observation", wantErr: "has no observed status"},
		{name: "paused", status: &v1.EndpointStatus{Phase: v1.EndpointPhasePAUSED}, wantErr: "is not Running (phase: Paused)"},
		{name: "deploying with 0 ready replicas (cold start or probe failed)", status: &v1.EndpointStatus{
			Phase: v1.EndpointPhaseDEPLOYING,
		}, wantErr: "has no ready replicas (phase: Deploying)"},
		{name: "pausing while still Running (replicas=0)", replicas: pointy.Int(0), status: &v1.EndpointStatus{
			Phase:     v1.EndpointPhaseRUNNING,
			Resources: &v1.EndpointResourceStatus{ReadyReplicas: 1},
		}, wantErr: "is paused"},
		{name: "deploying while pausing (replicas=0)", replicas: pointy.Int(0), status: &v1.EndpointStatus{
			Phase:     v1.EndpointPhaseDEPLOYING,
			Resources: &v1.EndpointResourceStatus{ReadyReplicas: 1},
		}, wantErr: "is paused"},
		{name: "deploying scaling with ready replicas (ReadyReplicas > 0)", status: &v1.EndpointStatus{
			Phase:     v1.EndpointPhaseDEPLOYING,
			Resources: &v1.EndpointResourceStatus{ReadyReplicas: 1},
		}, wantOK: true},
		{name: "deleting endpoint", status: &v1.EndpointStatus{Phase: v1.EndpointPhaseRUNNING}, endpointDeleted: true, wantErr: "is deleting"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ep := v1.Endpoint{
				Metadata: &v1.Metadata{Name: "ep", Workspace: "ws"},
				Spec:     &v1.EndpointSpec{Cluster: "c", Replicas: v1.ReplicaSpec{Num: tc.replicas}},
				Status:   tc.status,
			}
			cluster := v1.Cluster{
				Metadata: &v1.Metadata{},
				Spec:     &v1.ClusterSpec{Type: v1.KubernetesClusterType},
				Status:   &v1.ClusterStatus{DashboardURL: "http://router:8000"},
			}
			if tc.endpointDeleted {
				ep.Metadata.DeletionTimestamp = "2026-11-09T00:00:00Z"
			}
			s := storagemocks.NewMockStorage(t)
			s.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{ep}, nil)
			if tc.wantOK {
				s.On("ListCluster", mock.Anything).Return([]v1.Cluster{cluster}, nil)
			}
			_, _, _, _, err := (&Kong{storage: s}).resolveEndpointRef("ws", "ep")
			if tc.wantOK {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.ErrorContains(t, err, tc.wantErr)
			}
		})
	}
}
