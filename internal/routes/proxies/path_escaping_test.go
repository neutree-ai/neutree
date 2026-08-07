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

// These tests exist because routes.ConfigureEngine is an engine-wide switch and
// the proxies are the other family of route it reaches. They forward a
// caller-supplied path to the Ray dashboard, Ray Serve and the Kubernetes API,
// so what the router hands them, and what that turns into on the wire, is part
// of this API's contract with those upstreams — and nothing pinned it before.

// upstreamRecorder answers every request and records the path it was asked for,
// in both its decoded and its escaped form.
type upstreamRecorder struct {
	server      *httptest.Server
	requestURI  string
	decodedPath string
}

func newUpstreamRecorder(t *testing.T) *upstreamRecorder {
	t.Helper()

	recorder := &upstreamRecorder{}
	recorder.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// RequestURI is the raw bytes off the wire, before any decoding — the only
		// place the escaping the upstream actually receives is visible.
		recorder.requestURI = r.RequestURI
		recorder.decodedPath = r.URL.Path

		w.WriteHeader(http.StatusOK)
	}))

	t.Cleanup(recorder.server.Close)

	return recorder
}

// newProxyServer registers a wildcard proxy route shaped exactly like the real
// ones — `/:workspace/:name/*path`, the captured path stripped of its leading
// slash and handed to CreateProxyHandler — on an engine configured the way
// production configures it, and serves it over a real listener.
//
// A real listener rather than engine.ServeHTTP with a recorder: httputil's
// ReverseProxy requires a ResponseWriter that implements http.CloseNotifier, and
// httptest.ResponseRecorder does not. Going over the wire is the more faithful
// test anyway — the escaping under examination is a property of the bytes on the
// wire, and this way both ends of it are real.
//
// It reproduces the three real handlers rather than calling one of them:
// handleServeProxy, handleRayDashboardProxy and handleKubernetesProxy each look
// up a cluster before reaching this code, and the lookup is not what is under
// test. The two lines that are under test are identical in all three
// (proxies.go: `path := c.Param("path")`, leading slash trimmed, then
// CreateProxyHandler).
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
// url.Parse records it in RawPath, and the client writes that form to the wire.
func getRaw(t *testing.T, rawURL string) *http.Response {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	t.Cleanup(func() { resp.Body.Close() })

	return resp
}

// What a proxied path turns into on the wire, and — for each case — whether
// routes.ConfigureEngine changed it.
//
// Every case is run twice, through an engine with the setting and an engine
// without it, and the table records which ones differ. That is the claim worth
// pinning: the setting is engine-wide, so "it does not affect the proxies" has
// to be measured rather than argued, and the one case that does differ has to be
// visible rather than buried.
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
			// Arrives at the upstream as a real separator either way. The proxy
			// assigns req.URL.Path and never RawPath, so Go derives the wire form
			// from the decoded path whatever the router captured.
			wantUpstreamURI: "/logs/worker/out",
		},
		{
			name:        "double-encoded slash",
			requestPath: "/proxy/default/cluster/logs/a%252Fb",
			wantStatus:  http.StatusOK,
			// Decoded twice: once on the way out of the router, once more when
			// CreateProxyHandler interpolates the parameter into a URL string and
			// re-parses it. Pre-existing, and identical with and without the setting.
			wantUpstreamURI: "/logs/a/b",
		},
		{
			name:        "encoded percent",
			requestPath: "/proxy/default/cluster/logs/a%25b",
			// A lone "%" survives into the string CreateProxyHandler parses, which
			// rejects it as an invalid escape and answers 500. Pre-existing — the
			// same thing happens without the setting — and recorded here because it
			// is the sort of thing a later reader would otherwise blame on this
			// change.
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:        "encoded plus",
			requestPath: "/proxy/default/cluster/logs/a%2Bb",
			wantStatus:  http.StatusOK,
			// A literal "+" is a legal path character, so it needs no escaping on
			// the way out.
			wantUpstreamURI: "/logs/a+b",
		},
		{
			name:        "literal plus, no other escape",
			requestPath: "/proxy/default/cluster/logs/a+b",
			wantStatus:  http.StatusOK,
			// Unchanged, and the reason is worth stating: RawPath is only populated
			// when the escaped and decoded forms of a URL differ. A path whose only
			// notable character is "+" has no escapes at all, so RawPath is empty,
			// so gin falls back to URL.Path and the setting never comes into play.
			wantUpstreamURI: "/logs/a+b",
		},
		{
			name:        "literal plus alongside an escape",
			requestPath: "/proxy/default/cluster/logs/a+b%2Fc",
			wantStatus:  http.StatusOK,
			// The one real divergence, and it needs both ingredients: an escape
			// somewhere in the URL to make RawPath non-empty, and a literal "+" for
			// url.QueryUnescape to read as a space. Without the setting this reaches
			// the upstream as "/logs/a+b/c".
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
