package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTracesListPageBuildsQueryAndParsesResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/api/v1/ai-traces/default", r.URL.Path)
		require.Equal(t, "api-key", r.Header.Get("Authorization"))

		q := r.URL.Query()
		require.Equal(t, "100", q.Get("limit"))
		require.Equal(t, "my-ep", q.Get("endpoint_name"))
		require.Equal(t, "qwen", q.Get("model"))
		require.Equal(t, "2026-07-01T00:00:00Z", q.Get("start"))
		// before takes precedence over the filter's End.
		require.Equal(t, "2026-07-14T00:00:00Z", q.Get("before"))
		require.Empty(t, q.Get("end"))

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(aiTraceListResponse{
			Items:      []AITrace{{RequestID: "a"}, {RequestID: "b"}},
			NextBefore: "2026-07-13T00:00:00Z",
		})
	}))
	defer server.Close()

	c := NewClient(server.URL, WithAPIKey("api-key"))

	items, next, err := c.Traces.ListPage("default", TraceListFilters{
		EndpointName: "my-ep",
		Model:        "qwen",
		Start:        "2026-07-01T00:00:00Z",
		End:          "2026-07-10T00:00:00Z", // must be ignored when before is set
	}, "2026-07-14T00:00:00Z", 100)

	require.NoError(t, err)
	require.Equal(t, "2026-07-13T00:00:00Z", next)
	require.Len(t, items, 2)
	require.Equal(t, "a", items[0].RequestID)
}

func TestTracesListPageUsesEndWhenNoCursor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		require.Empty(t, q.Get("before"))
		require.Equal(t, "2026-07-10T00:00:00Z", q.Get("end"))
		require.Equal(t, "500", q.Get("limit")) // limit>max clamps to max

		_ = json.NewEncoder(w).Encode(aiTraceListResponse{})
	}))
	defer server.Close()

	c := NewClient(server.URL, WithAPIKey("k"))

	_, _, err := c.Traces.ListPage("default", TraceListFilters{End: "2026-07-10T00:00:00Z"}, "", 9999)
	require.NoError(t, err)
}

func TestTracesErrorStatusMapping(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{http.StatusServiceUnavailable, "access log store is not configured on the server (set --ai-trace-store-url)"},
		{http.StatusForbidden, "permission denied: the API key needs endpoint:trace-read or external_endpoint:trace-read"},
	}

	for _, tc := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
		}))

		c := NewClient(server.URL, WithAPIKey("k"))
		_, _, err := c.Traces.ListPage("default", TraceListFilters{}, "", 50)
		require.EqualError(t, err, tc.want)

		server.Close()
	}
}

func TestTracesGetDetail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/ai-traces/default/req-1", r.URL.Path)
		_ = json.NewEncoder(w).Encode(AITrace{RequestID: "req-1", RequestBody: "hello", ResponseBody: "world"})
	}))
	defer server.Close()

	c := NewClient(server.URL, WithAPIKey("k"))

	trace, err := c.Traces.GetDetail("default", "req-1")
	require.NoError(t, err)
	require.Equal(t, "hello", trace.RequestBody)
	require.Equal(t, "world", trace.ResponseBody)
}

func TestTracesListPageRequiresWorkspace(t *testing.T) {
	c := NewClient("http://example.com", WithAPIKey("k"))
	_, _, err := c.Traces.ListPage("", TraceListFilters{}, "", 50)
	require.Error(t, err)
}
