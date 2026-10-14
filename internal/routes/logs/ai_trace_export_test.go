package logs

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/tracecsv"
)

var exportLimitRe = regexp.MustCompile(`\| limit (\d+)`)

// exportStub is a VictoriaLogs stand-in that serves n traces, newest first,
// honoring the query's `| limit N` and the inclusive `end` param like the real
// store. failFrom makes every call from that index on return 500 (0 = never).
type exportStub struct {
	mu       sync.Mutex
	queries  []string
	params   []url.Values
	server   *httptest.Server
	failFrom int
}

func newExportStub(t *testing.T, n, failFrom int) *exportStub {
	t.Helper()

	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	s := &exportStub{failFrom: failFrom}

	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.queries = append(s.queries, r.URL.Query().Get("query"))
		s.params = append(s.params, r.URL.Query())
		call := len(s.queries)
		s.mu.Unlock()

		if s.failFrom > 0 && call >= s.failFrom {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		limit := n
		if m := exportLimitRe.FindStringSubmatch(r.URL.Query().Get("query")); m != nil {
			limit, _ = strconv.Atoi(m[1])
		}

		var end time.Time
		if v := r.URL.Query().Get("end"); v != "" {
			end, _ = time.Parse(time.RFC3339Nano, v)
		}

		w.Header().Set("Content-Type", "application/x-ndjson")

		written := 0

		for i := 0; i < n && written < limit; i++ {
			ts := base.Add(-time.Duration(i) * time.Second)
			if !end.IsZero() && ts.After(end) {
				continue
			}

			_, _ = fmt.Fprintf(w,
				`{"_time":%q,"request_id":"req-%d","workspace":"ws1","endpoint_type":"endpoint","endpoint_name":"e1","request_model":"模型","response_status":"200","total_tokens":"7"}`+"\n",
				ts.Format(time.RFC3339Nano), i)
			written++
		}
	}))
	t.Cleanup(s.server.Close)

	return s
}

func runExport(t *testing.T, stub *exportStub, workspace, rawQuery string, setup func(*gin.Context)) *httptest.ResponseRecorder {
	t.Helper()

	deps := &Dependencies{HTTPClient: &util.DefaultHTTPClient{}}
	if stub != nil {
		deps.AITraceStoreURL = stub.server.URL
	}

	c, w := traceCtx("user-1", workspace)
	c.Request = httptest.NewRequest("GET", "/?"+rawQuery, nil)

	if setup != nil {
		setup(c)
	}

	handleExportAITraces(deps)(c)

	return w
}

// jsonlIDs returns the request ids of a JSONL export body, in order.
func jsonlIDs(t *testing.T, body string) []string {
	t.Helper()

	ids := make([]string, 0)

	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		if line == "" {
			continue
		}

		var rec map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &rec))
		ids = append(ids, rec["request_id"].(string))
	}

	return ids
}

func TestHandleExportAITraces_Paging(t *testing.T) {
	cases := []struct {
		name        string
		records     int
		rawQuery    string
		wantRecords int
		wantCalls   int
	}{
		// The second page starts at the first page's last timestamp, so req-499
		// comes back and must be dropped.
		{"multi-page with boundary duplicate", 750, "format=jsonl", 750, 2},
		{"three pages", 1200, "format=jsonl", 1200, 3},
		{"limit within first page", 750, "format=jsonl&limit=3", 3, 1},
		{"limit across a page boundary", 750, "format=jsonl&limit=600", 600, 2},
		{"limit above max is clamped", 10, "format=jsonl&limit=999999", 10, 1},
		{"empty result", 0, "format=jsonl", 0, 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := newExportStub(t, tc.records, 0)

			w := runExport(t, stub, "ws1", tc.rawQuery, nil)

			require.Equal(t, http.StatusOK, w.Code)

			ids := jsonlIDs(t, w.Body.String())
			require.Len(t, ids, tc.wantRecords)

			for i, id := range ids {
				assert.Equal(t, fmt.Sprintf("req-%d", i), id, "newest first, no duplicates")
			}

			assert.Len(t, stub.queries, tc.wantCalls)

			if tc.wantCalls > 1 {
				// The next page's end is the previous page's last timestamp.
				assert.Equal(t, "2026-10-01T11:51:41Z", stub.params[1].Get("end"))
			}
		})
	}
}

func TestHandleExportAITraces_CSV(t *testing.T) {
	stub := newExportStub(t, 2, 0)

	w := runExport(t, stub, "ws1", "", nil)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "text/csv; charset=utf-8", w.Header().Get("Content-Type"))

	body := w.Body.String()
	require.True(t, strings.HasPrefix(body, "\xEF\xBB\xBF"), "csv starts with a UTF-8 BOM")

	lines := strings.Split(strings.TrimSpace(strings.TrimPrefix(body, "\xEF\xBB\xBF")), "\n")
	require.Len(t, lines, 3)
	assert.Equal(t, strings.Join(tracecsv.Header, ","), lines[0])
	assert.True(t, strings.HasPrefix(lines[1], "req-0,2026-10-01T12:00:00Z,ws1,endpoint,e1,,,模型,,200,,,7,"), lines[1])
}

func TestHandleExportAITraces_JSONLShape(t *testing.T) {
	stub := newExportStub(t, 1, 0)

	w := runExport(t, stub, "ws1", "format=jsonl", nil)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/x-ndjson", w.Header().Get("Content-Type"))

	var rec map[string]any
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(w.Body.String())), &rec))
	assert.Equal(t, map[string]any{
		"request_id":      "req-0",
		"time":            "2026-10-01T12:00:00Z",
		"workspace":       "ws1",
		"endpoint_type":   "endpoint",
		"endpoint_name":   "e1",
		"request_model":   "模型",
		"response_status": float64(200),
		"total_tokens":    float64(7),
		"stream":          false,
	}, rec)

	// Bodies are never fetched.
	assert.NotContains(t, stub.queries[0], "request_body")
}

func TestHandleExportAITraces_ContentDisposition(t *testing.T) {
	cases := []struct {
		workspace string
		rawQuery  string
		want      string
	}{
		{"ws1", "", `^attachment; filename="access-log-ws1-\d{8}T\d{6}\.csv"$`},
		{"ws1", "format=jsonl", `^attachment; filename="access-log-ws1-\d{8}T\d{6}\.jsonl"$`},
		{"_all_", "", `^attachment; filename="access-log-_all_-\d{8}T\d{6}\.csv"$`},
		{`a"b`, "", `^attachment; filename="access-log-a_b-\d{8}T\d{6}\.csv"$`},
	}

	for _, tc := range cases {
		t.Run(tc.workspace+" "+tc.rawQuery, func(t *testing.T) {
			stub := newExportStub(t, 1, 0)

			w := runExport(t, stub, tc.workspace, tc.rawQuery, func(c *gin.Context) {
				if tc.workspace == allWorkspacesSentinel {
					c.Set(traceScopeFilterKey, "*")
				}
			})

			require.Equal(t, http.StatusOK, w.Code)
			assert.Regexp(t, tc.want, w.Header().Get("Content-Disposition"))
		})
	}
}

func TestHandleExportAITraces_ScopeAndFilters(t *testing.T) {
	cases := []struct {
		name      string
		workspace string
		rawQuery  string
		setup     func(*gin.Context)
		wantQuery []string
		wantStart string
		wantEnd   string
	}{
		{
			name:      "workspace restricted to one endpoint type",
			workspace: "ws1",
			rawQuery:  "endpoint_name=e1&model=m1&status=200&api_key_id=k1&start=2026-09-01T00:00:00Z&end=2026-10-01T00:00:00Z",
			setup:     func(c *gin.Context) { c.Set(traceEndpointTypeKey, "endpoint") },
			wantQuery: []string{
				`workspace:="ws1" endpoint_type:="endpoint"`,
				`endpoint_name:="e1"`,
				`(request_model:="m1" OR response_model:="m1")`,
				`response_status:="200"`,
				`api_key_id:="k1"`,
			},
			wantStart: "2026-09-01T00:00:00Z",
			wantEnd:   "2026-10-01T00:00:00Z",
		},
		{
			name:      "all workspaces uses the precomputed scope; before overrides end",
			workspace: allWorkspacesSentinel,
			rawQuery:  "upstream=u1&before=2026-10-01T06:00:00Z&end=2026-10-02T00:00:00Z",
			setup:     func(c *gin.Context) { c.Set(traceScopeFilterKey, `((workspace:="ws1"))`) },
			wantQuery: []string{`((workspace:="ws1"))`, `upstream:="u1"`},
			wantEnd:   "2026-10-01T06:00:00Z",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := newExportStub(t, 0, 0)

			w := runExport(t, stub, tc.workspace, tc.rawQuery, tc.setup)

			require.Equal(t, http.StatusOK, w.Code)
			require.Len(t, stub.queries, 1)

			for _, want := range tc.wantQuery {
				assert.Contains(t, stub.queries[0], want)
			}

			assert.Equal(t, tc.wantStart, stub.params[0].Get("start"))
			assert.Equal(t, tc.wantEnd, stub.params[0].Get("end"))
		})
	}
}

func TestHandleExportAITraces_ErrorsBeforeStreaming(t *testing.T) {
	cases := []struct {
		name     string
		noStore  bool
		failFrom int
		rawQuery string
		wantCode int
	}{
		{"store not configured", true, 0, "", http.StatusServiceUnavailable},
		{"unknown format", false, 0, "format=xml", http.StatusBadRequest},
		{"non-numeric limit", false, 0, "limit=abc", http.StatusBadRequest},
		{"zero limit", false, 0, "limit=0", http.StatusBadRequest},
		{"first page fails", false, 1, "", http.StatusBadGateway},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stub *exportStub
			if !tc.noStore {
				stub = newExportStub(t, 10, tc.failFrom)
			}

			w := runExport(t, stub, "ws1", tc.rawQuery, nil)

			assert.Equal(t, tc.wantCode, w.Code)
			assert.Empty(t, w.Header().Get("Content-Disposition"))

			var resp map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.NotEmpty(t, resp["error"])
		})
	}
}

func TestHandleExportAITraces_ErrorAfterStreamingStops(t *testing.T) {
	stub := newExportStub(t, 750, 2)

	w := runExport(t, stub, "ws1", "format=jsonl", nil)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Len(t, jsonlIDs(t, w.Body.String()), maxTracePageSize)
}

func TestExportRoute_RequiresTracePermission(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	setUser := func(c *gin.Context) { c.Set("user_id", "user-1") }
	RegisterAITraceRoutes(r.Group("/api/v1"), []gin.HandlerFunc{setUser},
		&Dependencies{Storage: permMock(map[string]bool{})})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/ai-traces/ws1/export", nil))

	assert.Equal(t, http.StatusForbidden, w.Code)
}
