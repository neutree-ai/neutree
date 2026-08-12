package proxies

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/neutree-ai/neutree/internal/routes"
)

// The proxy routes forward a caller-supplied path to the Ray dashboard, Ray Serve
// and the Kubernetes API, so what routes.ConfigureEngine does to that path is part
// of this API's contract with those upstreams.

// upstreamRecorder answers every request and records the path it was asked for.
type upstreamRecorder struct {
	server      *httptest.Server
	requestURI  string
	decodedPath string
}

func newUpstreamRecorder(t *testing.T) *upstreamRecorder {
	t.Helper()

	recorder := &upstreamRecorder{}
	recorder.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// RequestURI is the raw request line, the only place the escaping the upstream
		// receives is visible.
		recorder.requestURI = r.RequestURI
		recorder.decodedPath = r.URL.Path

		w.WriteHeader(http.StatusOK)
	}))

	t.Cleanup(recorder.server.Close)

	return recorder
}

// newProxyServer serves a wildcard proxy route shaped like the real ones over a
// real listener. httputil.ReverseProxy needs a ResponseWriter implementing
// http.CloseNotifier, which httptest.ResponseRecorder is not.
//
// It reproduces the handler body rather than calling handleServeProxy,
// handleRayDashboardProxy or handleKubernetesProxy: each looks up a cluster
// first, and the two lines under test are identical in all three.
func newProxyServer(t *testing.T, upstreamURL string, configured bool) *httptest.Server {
	t.Helper()
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	if configured {
		routes.ConfigureEngine(engine)
	}

	engine.Any("/proxy/:workspace/:name/*path", func(c *gin.Context) {
		path := c.Param("path")
		if path != "" && path[0] == '/' {
			path = path[1:]
		}

		CreateProxyHandler(upstreamURL, path, nil)(c)
	})

	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)

	return server
}

// getRaw sends a request whose path keeps the escaping exactly as written.
func getRaw(t *testing.T, rawURL string) *http.Response {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	t.Cleanup(func() { resp.Body.Close() })

	return resp
}

// What a proxied path turns into on the wire, and which cases
// routes.ConfigureEngine changes. Every case runs through an engine with the
// setting and one without, so a change in blast radius fails here.
func TestProxyPathEscaping(t *testing.T) {
	tests := []struct {
		name string
		// requestPath is written with the escaping the client sends.
		requestPath string
		wantStatus  int
		// wantUpstreamURI is the raw request line the upstream receives. Empty when
		// the request never reaches it.
		wantUpstreamURI string
		// differs records whether the setting changed this case.
		differs bool
		note    string
	}{
		{
			name:            "plain path",
			requestPath:     "/proxy/default/cluster/api/version",
			wantStatus:      http.StatusOK,
			wantUpstreamURI: "/api/version",
		},
		{
			name:        "encoded slash",
			requestPath: "/proxy/default/cluster/logs/worker%2Fout",
			wantStatus:  http.StatusOK,
			// A real separator either way: the proxy assigns req.URL.Path and never RawPath,
			// so the wire form is derived from the decoded path.
			wantUpstreamURI: "/logs/worker/out",
		},
		{
			name:        "double-encoded slash",
			requestPath: "/proxy/default/cluster/logs/a%252Fb",
			wantStatus:  http.StatusOK,
			// Decoded twice — once by the router, once when CreateProxyHandler re-parses the
			// parameter into a URL. Pre-existing.
			wantUpstreamURI: "/logs/a/b",
		},
		{
			name:        "encoded percent",
			requestPath: "/proxy/default/cluster/logs/a%25b",
			// A lone "%" reaches url.Parse as an invalid escape. Pre-existing, and recorded
			// so it is not later blamed on the setting.
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:        "encoded plus",
			requestPath: "/proxy/default/cluster/logs/a%2Bb",
			wantStatus:  http.StatusOK,
			// A literal "+" is a legal path character and needs no escaping.
			wantUpstreamURI: "/logs/a+b",
		},
		{
			name:        "literal plus, no other escape",
			requestPath: "/proxy/default/cluster/logs/a+b",
			wantStatus:  http.StatusOK,
			// RawPath is only populated when a URL's escaped and decoded forms differ, so a
			// path with no escapes never engages the setting at all.
			wantUpstreamURI: "/logs/a+b",
		},
		{
			name:        "literal plus alongside an escape",
			requestPath: "/proxy/default/cluster/logs/a+b%2Fc",
			wantStatus:  http.StatusOK,
			// The one divergence, and it needs both an escape (to populate RawPath) and a
			// literal "+" (which QueryUnescape reads as a space).
			wantUpstreamURI: "/logs/a%20b/c",
			differs:         true,
			note:            "known divergence introduced by routes.ConfigureEngine",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configured := newUpstreamRecorder(t)
			configuredResp := getRaw(t, newProxyServer(t, configured.server.URL, true).URL+tt.requestPath)

			require.Equal(t, tt.wantStatus, configuredResp.StatusCode)
			assert.Equal(t, tt.wantUpstreamURI, configured.requestURI, tt.note)

			before := newUpstreamRecorder(t)
			beforeResp := getRaw(t, newProxyServer(t, before.server.URL, false).URL+tt.requestPath)

			if tt.differs {
				assert.NotEqual(t, before.requestURI, configured.requestURI,
					"this case is recorded as changed by the setting; it did not change")

				return
			}

			assert.Equal(t, before.requestURI, configured.requestURI,
				"the setting must not change what this proxied path becomes")
			assert.Equal(t, beforeResp.StatusCode, configuredResp.StatusCode)
		})
	}
}

// The single-segment proxy route, which has no cluster lookup in front of it and
// so can be driven exactly as it is registered. An encoded slash inside an RPC
// name used to make the request one segment too long and miss the route
// entirely; now it routes and the name is passed through decoded.
func TestPostgrestRPCProxyRoutesAnEncodedSlash(t *testing.T) {
	upstream := newUpstreamRecorder(t)

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	routes.ConfigureEngine(engine)
	RegisterPostgrestRPCProxyRoutes(&engine.RouterGroup, nil, &Dependencies{
		StorageAccessURL: upstream.server.URL,
	})

	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)

	resp := getRaw(t, server.URL+"/rpc/get_workspace_models")

	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "/rpc/get_workspace_models", upstream.requestURI)
}
