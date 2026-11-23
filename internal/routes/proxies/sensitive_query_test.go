package proxies

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

const leakedSecret = "plaintext-secret"

// leakingUpstream stands in for PostgREST evaluating a query that projects a
// masked field under a shape the response filter does not recognise.
func leakingUpstream(t *testing.T, called *atomic.Int32) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"pw":"` + leakedSecret + `"}]`))
	}))
	t.Cleanup(server.Close)

	return server
}

type queryCase struct {
	name  string
	query string
}

// structProxyFactory builds a struct proxy handler on group.
type structProxyFactory func(group gin.IRoutes, deps *Dependencies) gin.HandlerFunc

func structHandler[T any](table string) structProxyFactory {
	return func(group gin.IRoutes, deps *Dependencies) gin.HandlerFunc {
		return CreateStructProxyHandler[T](group, deps, table)
	}
}

type resourceQueryCases struct {
	name    string
	handler structProxyFactory
	cases   []queryCase
}

func serveQuery(t *testing.T, factory structProxyFactory, deps *Dependencies, method, query string) *closeNotifyRecorder {
	t.Helper()

	router := gin.New()
	router.Handle(method, "/api/v1/resource", factory(router, deps))

	var body *strings.Reader
	if method == http.MethodGet {
		body = strings.NewReader("")
	} else {
		body = strings.NewReader(`{}`)
	}

	req := httptest.NewRequest(method, "/api/v1/resource?"+query, body)
	req.Header.Set("Content-Type", "application/json")

	recorder := newCloseNotifyRecorder()
	router.ServeHTTP(recorder, req)

	return recorder
}

func TestStructProxyRejectsQueriesReachingMaskedFields(t *testing.T) {
	gin.SetMode(gin.TestMode)

	resources := []resourceQueryCases{
		{
			name:    "log_sinks",
			handler: structHandler[v1.LogSink](storage.LOG_SINK_TABLE),
			cases: []queryCase{
				{"alias over json path", "select=pw:spec->victorialogs->auth->>password"},
				{"json path to secret parent", "select=spec->victorialogs->auth"},
				{"json path to secret", "select=spec->victorialogs->auth->>password"},
				{"json path without alias to sibling", "select=spec->>type"},
				{"alias of a top-level column", "select=s:spec"},
				{"cast", "select=spec::text"},
				{"cast of json path", "select=spec->victorialogs::text"},
				{"embedded resource", "select=*,other(*)"},
				{"spread", "select=...other(*)"},
				{"empty select", "select="},
				{"filter oracle", "spec->victorialogs->auth->>password=like.a*"},
				{"filter oracle on secret parent", "spec->victorialogs->auth=cs.{}"},
				{"filter on top-level column containing secret", "spec=cs.{}"},
				{"quoted filter key", `"spec"->"victorialogs"->auth->>password=like.a*`},
				{"case-folded filter key", "SPEC->victorialogs->auth->>PASSWORD=like.a*"},
				{"embedded filter key", "other.spec=eq.1"},
				{"order by secret", "order=spec->victorialogs->auth->>password.asc"},
				{"order by second term secret", "order=id.asc,spec->victorialogs->auth->>token.desc"},
				{"order by top-level column containing secret", "order=spec"},
				{"or tree", "or=(spec->victorialogs->auth->>password.like.a*,id.eq.0)"},
				{"and tree", "and=(id.gt.0,spec->victorialogs->auth->>token.like.a*)"},
				{"negated or tree", "not.or=(spec->victorialogs->auth->>password.like.a*,id.eq.0)"},
				{"nested tree", "or=(id.eq.0,and(id.gt.0,not.or(spec->victorialogs->auth->>password.like.a*)))"},
				{"tree hidden behind a quoted value", `or=(metadata->>name.eq."x",spec->victorialogs->auth->>password.like.a*)`},
				{"tree hidden behind an array value", "or=(metadata->labels.cs.{a,spec->victorialogs->auth->>password.like.a*})"},
				{"embedded tree key", "other.or=(id.eq.1)"},
				{"tree element after whitespace", "or=(id.eq.1,%0Aspec->victorialogs->auth->>password.like.a*)"},
				{"tree element after not", "or=(not.spec->victorialogs->auth->>password.like.a*)"},
				{"escaped quoted filter key", `"sp%5Cec"->victorialogs->auth->>password=like.a*`},
				{"array index in path", "spec->victorialogs->0->auth->>password=like.a*"},
				{"related order", "order=other(spec).asc"},
				{"semicolon separated params", "id=eq.1;spec->victorialogs->auth->>password=like.a*"},
			},
		},
		{
			name:    "image_registries",
			handler: structHandler[v1.ImageRegistry](storage.IMAGE_REGISTRY_TABLE),
			cases: []queryCase{
				{"json path to masked object", "select=spec->authconfig"},
				{"filter on masked object", "spec->authconfig->>password=like.a*"},
				{"filter on masked object root", "spec->authconfig=not.is.null"},
			},
		},
		{
			name:    "model_registries",
			handler: structHandler[v1.ModelRegistry](storage.MODEL_REGISTRY_TABLE),
			cases: []queryCase{
				{"json path to credentials", "select=c:spec->>credentials"},
				{"filter on credentials", "spec->>credentials=like.hf_*"},
			},
		},
		{
			name:    "clusters",
			handler: structHandler[v1.Cluster](storage.CLUSTERS_TABLE),
			cases: []queryCase{
				{"json path to kubeconfig", "select=spec->config->kubernetes_config->>kubeconfig"},
				{"filter on ssh key", "spec->config->ssh_config->auth->>ssh_private_key=like.-----*"},
			},
		},
		{
			name:    "api_keys",
			handler: structHandler[v1.ApiKey]("api_keys"),
			cases: []queryCase{
				{"json path to sk_value", "select=status->>sk_value"},
				{"filter on sk_value", "status->>sk_value=like.sk-*"},
				{"filter on status", "status=cs.{}"},
			},
		},
	}

	for _, resource := range resources {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete} {
			for _, tc := range resource.cases {
				t.Run(resource.name+"/"+method+"/"+tc.name, func(t *testing.T) {
					var called atomic.Int32

					upstream := leakingUpstream(t, &called)
					deps := &Dependencies{
						StorageAccessURL: upstream.URL,
					}

					recorder := serveQuery(t, resource.handler, deps, method, tc.query)

					assert.Equal(t, http.StatusBadRequest, recorder.ResponseRecorder.Code, recorder.Body.String())
					assert.NotContains(t, recorder.Body.String(), leakedSecret)
					assert.Zero(t, called.Load(), "request must not reach PostgREST")

					var errBody map[string]interface{}
					require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &errBody))
					assert.NotEmpty(t, errBody["message"])
				})
			}
		}
	}
}

func TestStructProxyAllowsQueriesAwayFromMaskedFields(t *testing.T) {
	gin.SetMode(gin.TestMode)

	resources := []resourceQueryCases{
		{
			name:    "model_registries",
			handler: structHandler[v1.ModelRegistry](storage.MODEL_REGISTRY_TABLE),
			cases: []queryCase{
				{"select all", "select=*"},
				{"select with computed field", "select=*,visibility"},
				{"select with spaces", "select=*,%20visibility"},
				{"select top-level column", "select=metadata"},
				{"select top-level column containing secret", "select=spec"},
				{"select count", "select=count()"},
				{"name filter", "metadata->>name=eq.x"},
				{"workspace json filter", `metadata->workspace=eq."x"`},
				{"type filter", "spec->>type=eq.hugging-face"},
				{"id filter", "id=eq.1"},
				{"pagination", "limit=10&offset=20"},
				{"order by metadata", "order=metadata->name.asc,metadata->creation_timestamp.desc.nullslast"},
				{"or tree away from secrets", `or=(metadata->>name.eq.x,metadata->>name.in.("a,b",c),and(id.gt.1,spec->>type.eq.bentoml))`},
				{"negated or tree away from secrets", "not.or=(metadata->>name.eq.x,id.eq.1)"},
				{"on_conflict and columns", "on_conflict=id&columns=metadata,spec"},
				{"quoted json key", `metadata->labels->>"example.com/team"=eq.a`},
			},
		},
		{
			name:    "clusters",
			handler: structHandler[v1.Cluster](storage.CLUSTERS_TABLE),
			cases: []queryCase{
				{"type filter", "spec->>type=eq.ssh"},
				{"image registry filter", "spec->>image_registry=eq.r"},
				{"status order", "order=status_sort_priority.asc"},
			},
		},
		{
			name:    "api_keys",
			handler: structHandler[v1.ApiKey]("api_keys"),
			cases: []queryCase{
				{"user filter", `spec->user_id=eq."u"`},
				{"global filter", "spec->global=eq.true"},
				{"global text filter", "spec->>global=eq.true"},
				{"workspace filter", `spec->workspace=eq."default"`},
				{"phase filter", "status->>phase=eq.Created"},
			},
		},
		{
			// Without masked fields the guard does not apply at all.
			name:    "endpoints",
			handler: structHandler[v1.Endpoint](storage.ENDPOINT_TABLE),
			cases: []queryCase{
				{"cluster filter", "spec->>cluster=eq.c"},
				{"cluster json filter", `spec->cluster=eq."c"`},
				{"task filter", "spec->model->>task=eq.text-generation"},
				{"alias", "select=n:metadata->>name"},
				{"cast", "select=spec::text"},
			},
		},
	}

	for _, resource := range resources {
		for _, tc := range resource.cases {
			t.Run(resource.name+"/"+tc.name, func(t *testing.T) {
				var called atomic.Int32

				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					called.Add(1)
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`[]`))
				}))
				defer upstream.Close()

				recorder := serveQuery(t, resource.handler, &Dependencies{StorageAccessURL: upstream.URL}, http.MethodGet, tc.query)

				assert.Equal(t, http.StatusOK, recorder.ResponseRecorder.Code, recorder.Body.String())
				assert.Equal(t, int32(1), called.Load())
			})
		}
	}
}

const logSinkRowWithSecrets = `[{
	"id": 7,
	"metadata": {"name": "vl"},
	"spec": {"type": "victorialogs", "victorialogs": {
		"url": "https://vl.example.com",
		"auth": {"type": "basic", "username": "writer", "password": "` + leakedSecret + `", "token": "` + leakedSecret + `"}
	}}
}]`

func TestStructProxyMasksWriteRepresentations(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name   string
		method string
		body   string
		status int
	}{
		{
			name:   "post",
			method: http.MethodPost,
			body:   `{"metadata":{"name":"vl"},"spec":{"type":"victorialogs","victorialogs":{"url":"https://vl.example.com","auth":{"type":"basic","username":"writer","password":"pw"}}}}`,
			status: http.StatusCreated,
		},
		{
			name:   "patch with backfill",
			method: http.MethodPatch,
			body:   `{"spec":{"type":"victorialogs","victorialogs":{"url":"https://vl2.example.com","auth":{"type":"basic","username":"writer"}}}}`,
			status: http.StatusOK,
		},
		{
			name:   "soft delete patch",
			method: http.MethodPatch,
			body:   `{"metadata":{"name":"vl","deletion_timestamp":"2026-05-28T07:20:48Z"}}`,
			status: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.method == http.MethodPatch && serveBackfillLookup(w, r) {
					return
				}

				assert.Equal(t, tt.method, r.Method)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(logSinkRowWithSecrets))
			}))
			defer upstream.Close()

			router := gin.New()
			RegisterLogSinkRoutes(router.Group("/api/v1"), nil, &Dependencies{
				StorageAccessURL: upstream.URL,
			})

			req := httptest.NewRequest(tt.method, "/api/v1/log_sinks?id=eq.7&select=*", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Prefer", "return=representation")

			recorder := newCloseNotifyRecorder()
			router.ServeHTTP(recorder, req)

			require.Equal(t, tt.status, recorder.ResponseRecorder.Code, recorder.Body.String())
			assert.NotContains(t, recorder.Body.String(), leakedSecret)

			var sinks []v1.LogSink
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &sinks))
			require.Len(t, sinks, 1)
			assert.Equal(t, "writer", sinks[0].Spec.VictoriaLogs.Auth.Username)
		})
	}
}

// The cluster PATCH validation resolves the target row with the service role
// before the proxy handler runs, so the guard must reject first.
func TestClusterRoutesRejectMaskedFieldFilterBeforeValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var called atomic.Int32

	upstream := leakingUpstream(t, &called)

	router := gin.New()
	RegisterClusterRoutes(router.Group("/api/v1"), nil, &Dependencies{
		StorageAccessURL: upstream.URL,
	})

	req := httptest.NewRequest(
		http.MethodPatch,
		"/api/v1/clusters?spec->config->kubernetes_config->>kubeconfig=like.a*",
		strings.NewReader(`{"spec":{"type":"ssh"}}`),
	)
	req.Header.Set("Content-Type", "application/json")

	recorder := newCloseNotifyRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusBadRequest, recorder.ResponseRecorder.Code)
	assert.Contains(t, recorder.Body.String(), sensitiveQueryRejectedCode)
	assert.Zero(t, called.Load())
}

// The guard is installed on the route group, so it rejects before any
// per-route validator reads the query or body.
func TestRegisteredRoutesRejectMaskedFieldQueriesBeforeValidators(t *testing.T) {
	gin.SetMode(gin.TestMode)

	routes := []struct {
		name     string
		register func(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies)
		path     string
		filter   string
	}{
		{"clusters", RegisterClusterRoutes, "clusters", "spec=cs.{}"},
		{"external_endpoints", RegisterExternalEndpointRoutes, "external_endpoints", "spec->upstreams=cs.[]"},
		{"identity_sources", RegisterIdentitySourceRoutes, "identity_sources", "spec=cs.{}"},
		{"image_registries", RegisterImageRegistryRoutes, "image_registries", "spec->authconfig=not.is.null"},
		{"log_sinks", RegisterLogSinkRoutes, "log_sinks", "spec=cs.{}"},
		{"model_registries", RegisterModelRegistryRoutes, "model_registries", "spec->>credentials=like.a*"},
		{"api_keys", RegisterAPIKeyRoutes, "api_keys", "status->>sk_value=like.sk-*"},
		{"static_node_clusters", RegisterStaticNodeClusterRoutes, "static_node_clusters", "spec=cs.{}"},
	}

	for _, route := range routes {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPatch} {
			t.Run(route.name+"/"+method, func(t *testing.T) {
				var called atomic.Int32

				upstream := leakingUpstream(t, &called)

				router := gin.New()
				route.register(router.Group("/api/v1"), nil, &Dependencies{StorageAccessURL: upstream.URL})

				// A malformed body makes any validator that runs first fail with its own error.
				req := httptest.NewRequest(method, "/api/v1/"+route.path+"?"+route.filter, strings.NewReader("{"))
				req.Header.Set("Content-Type", "application/json")

				recorder := newCloseNotifyRecorder()
				router.ServeHTTP(recorder, req)

				if recorder.ResponseRecorder.Code == http.StatusNotFound || recorder.ResponseRecorder.Code == http.StatusMethodNotAllowed {
					t.Skipf("%s is not routed", method)
				}

				assert.Equal(t, http.StatusBadRequest, recorder.ResponseRecorder.Code, recorder.Body.String())
				assert.Contains(t, recorder.Body.String(), sensitiveQueryRejectedCode)
				assert.Zero(t, called.Load())
			})
		}
	}
}
