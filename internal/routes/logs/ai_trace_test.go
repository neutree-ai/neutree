package logs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/storage/mocks"
)

// statsCtx builds a gin.Context whose request carries the given query string.
func statsCtx(rawQuery string) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/?"+rawQuery, nil)

	return c
}

func TestStatsWindow_ExplicitStartEnd(t *testing.T) {
	q := url.Values{}
	q.Set("start", "2026-03-01T08:30:00Z")
	q.Set("end", "2026-03-05T23:00:00Z")

	startDay, endDay, ok := statsWindow(statsCtx(q.Encode()))

	assert.True(t, ok)
	// Both bounds truncate to their UTC day.
	assert.Equal(t, "2026-03-01", startDay.Format("2006-01-02"))
	assert.Equal(t, "2026-03-05", endDay.Format("2006-01-02"))
	assert.Equal(t, time.UTC, startDay.Location())
}

func TestStatsWindow_EndBeforeStartRejected(t *testing.T) {
	q := url.Values{}
	q.Set("start", "2026-03-10T00:00:00Z")
	q.Set("end", "2026-03-01T00:00:00Z")

	_, _, ok := statsWindow(statsCtx(q.Encode()))
	assert.False(t, ok)
}

func TestStatsWindow_MalformedRejected(t *testing.T) {
	_, _, ok := statsWindow(statsCtx("start=not-a-time"))
	assert.False(t, ok)
}

func TestStatsWindow_ClampedToMaxDays(t *testing.T) {
	q := url.Values{}
	q.Set("start", "2026-01-01T00:00:00Z")
	q.Set("end", "2026-06-01T00:00:00Z") // far more than maxStatsDays apart

	startDay, endDay, ok := statsWindow(statsCtx(q.Encode()))

	assert.True(t, ok)
	// Span clamped to maxStatsDays buckets, anchored at the most recent end.
	bucketCount := int(endDay.Sub(startDay).Hours()/24) + 1
	assert.Equal(t, maxStatsDays, bucketCount)
	assert.Equal(t, "2026-06-01", endDay.Format("2006-01-02"))
}

func TestStatsWindow_DaysFallback(t *testing.T) {
	startDay, endDay, ok := statsWindow(statsCtx("days=30"))

	assert.True(t, ok)
	bucketCount := int(endDay.Sub(startDay).Hours()/24) + 1
	assert.Equal(t, 30, bucketCount)
	// endDay is today's UTC bucket.
	assert.Equal(t, time.Now().UTC().Truncate(24*time.Hour).Format("2006-01-02"), endDay.Format("2006-01-02"))
}

func TestStatsWindow_DefaultSevenDays(t *testing.T) {
	startDay, endDay, ok := statsWindow(statsCtx(""))

	assert.True(t, ok)
	bucketCount := int(endDay.Sub(startDay).Hours()/24) + 1
	assert.Equal(t, 7, bucketCount)
}

func TestStatsWindow_DaysOutOfRangeFallsBackToDefault(t *testing.T) {
	// >maxStatsDays is ignored, falling back to the 7-day default.
	startDay, endDay, ok := statsWindow(statsCtx("days=999"))

	assert.True(t, ok)
	bucketCount := int(endDay.Sub(startDay).Hours()/24) + 1
	assert.Equal(t, 7, bucketCount)
}

func TestStatsWindow_EndOnlyTrailingWindow(t *testing.T) {
	// `end` without `start` is a valid trailing window (default 7 days ending at
	// `end`), not a 400. Regression guard for the end-only handling.
	q := url.Values{}
	q.Set("end", "2026-03-10T12:00:00Z")

	startDay, endDay, ok := statsWindow(statsCtx(q.Encode()))

	assert.True(t, ok)
	assert.Equal(t, "2026-03-10", endDay.Format("2006-01-02"))
	assert.Equal(t, "2026-03-04", startDay.Format("2006-01-02")) // 7 buckets inclusive
}

func TestStatsWindow_EndOnlyWithDays(t *testing.T) {
	q := url.Values{}
	q.Set("end", "2026-03-10T00:00:00Z")
	q.Set("days", "3")

	startDay, endDay, ok := statsWindow(statsCtx(q.Encode()))

	assert.True(t, ok)
	bucketCount := int(endDay.Sub(startDay).Hours()/24) + 1
	assert.Equal(t, 3, bucketCount)
	assert.Equal(t, "2026-03-10", endDay.Format("2006-01-02"))
}

func TestStatsWindow_MalformedEndRejected(t *testing.T) {
	q := url.Values{}
	q.Set("end", "not-a-time")

	_, _, ok := statsWindow(statsCtx(q.Encode()))
	assert.False(t, ok)
}

func TestLogsQLQuoteValue(t *testing.T) {
	cases := map[string]string{
		"":                   `""`,
		"plain":              `"plain"`,
		`with"quote`:         `"with\"quote"`,
		`back\slash`:         `"back\\slash"`,
		"line\nbreak":        `"line\nbreak"`,
		`a"b\c` + "\n" + "d": `"a\"b\\c\nd"`,
	}
	for in, want := range cases {
		assert.Equalf(t, want, logsQLQuoteValue(in), "input %q", in)
	}
}

func TestTraceEndpointTypeFilter(t *testing.T) {
	// Unrestricted: no endpoint-type key set => no clause.
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	assert.Equal(t, "", traceEndpointTypeFilter(c))

	// Restricted to a single endpoint type => exact-match LogsQL clause.
	c.Set(traceEndpointTypeKey, "external-endpoint")
	assert.Equal(t, `endpoint_type:="external-endpoint"`, traceEndpointTypeFilter(c))
}

func TestDecodeVLRecord_FullRecord(t *testing.T) {
	line := []byte(`{
		"_time":"2026-03-01T10:00:00Z",
		"request_id":"req-1",
		"workspace":"ws1",
		"endpoint_type":"endpoint",
		"endpoint_name":"gpt",
		"api_key_id":"key-1",
		"request_uri":"/v1/chat",
		"request_model":"gpt-4",
		"response_model":"gpt-4-0613",
		"response_status":"200",
		"prompt_tokens":"10",
		"completion_tokens":"20",
		"total_tokens":"30",
		"finish_reason":"stop",
		"stream":"true",
		"user_agent":"openai-python",
		"duration_ms":"1234.5",
		"request_body":"req",
		"response_body":"resp"
	}`)

	tr, ok := decodeVLRecord(line)

	assert.True(t, ok)
	assert.Equal(t, "req-1", tr.RequestID)
	assert.Equal(t, "ws1", tr.Workspace)
	assert.Equal(t, 200, tr.ResponseStatus)
	assert.Equal(t, "stop", tr.FinishReason)
	assert.True(t, tr.Stream)
	if assert.NotNil(t, tr.PromptTokens) {
		assert.Equal(t, 10, *tr.PromptTokens)
	}
	if assert.NotNil(t, tr.TotalTokens) {
		assert.Equal(t, 30, *tr.TotalTokens)
	}
	if assert.NotNil(t, tr.DurationMs) {
		assert.Equal(t, 1234, *tr.DurationMs) // float string truncated to int ms
	}
	assert.Equal(t, "req", tr.RequestBody)
	assert.Equal(t, "resp", tr.ResponseBody)
}

func TestDecodeVLRecord_OptionalFieldsAbsent(t *testing.T) {
	// A minimal record (e.g. an early-returning error response): optional numeric
	// columns must stay nil rather than coerce to a misleading 0.
	line := []byte(`{"_time":"2026-03-01T10:00:00Z","request_id":"req-2","workspace":"ws1"}`)

	tr, ok := decodeVLRecord(line)

	assert.True(t, ok)
	assert.Equal(t, "req-2", tr.RequestID)
	assert.Equal(t, 0, tr.ResponseStatus)
	assert.False(t, tr.Stream)
	assert.Nil(t, tr.PromptTokens)
	assert.Nil(t, tr.CompletionTokens)
	assert.Nil(t, tr.TotalTokens)
	assert.Nil(t, tr.DurationMs)
}

func TestDecodeVLRecord_MissingTimeFallsBack(t *testing.T) {
	line := []byte(`{"request_id":"req-3","workspace":"ws1"}`)

	tr, ok := decodeVLRecord(line)

	assert.True(t, ok)
	assert.NotEmpty(t, tr.Time) // synthesised so the UI always has a timestamp
}

func TestDecodeVLRecord_InvalidJSON(t *testing.T) {
	_, ok := decodeVLRecord([]byte(`{not json`))
	assert.False(t, ok)
}

// traceCtx builds a gin context + recorder carrying the given user id and
// workspace param, as the route middleware would have populated them.
func traceCtx(userID, workspace string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/", nil)

	if userID != "" {
		c.Set("user_id", userID)
	}

	if workspace != "" {
		c.Params = gin.Params{{Key: "workspace", Value: workspace}}
	}

	return c, w
}

// permMock returns a MockStorage whose has_permission call resolves to the
// allow value matching the required_permission argument.
func permMock(allow map[string]bool) *mocks.MockStorage {
	m := new(mocks.MockStorage)
	m.On("CallDatabaseFunction", "has_permission", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			params := args.Get(1).(map[string]interface{})
			perm, _ := params["required_permission"].(string)
			out := args.Get(2).(*bool)
			*out = allow[perm]
		}).Return(nil)

	return m
}

func TestRequireTracePermission_Unauthenticated(t *testing.T) {
	deps := &Dependencies{Storage: new(mocks.MockStorage)}
	c, w := traceCtx("", "ws1")

	requireTracePermission(deps)(c)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.True(t, c.IsAborted())
}

func TestRequireTracePermission_MissingWorkspace(t *testing.T) {
	deps := &Dependencies{Storage: new(mocks.MockStorage)}
	c, w := traceCtx("user-1", "")

	requireTracePermission(deps)(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.True(t, c.IsAborted())
}

func TestRequireTracePermission_BothDeniedForbidden(t *testing.T) {
	deps := &Dependencies{Storage: permMock(map[string]bool{})}
	c, w := traceCtx("user-1", "ws1")

	requireTracePermission(deps)(c)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.True(t, c.IsAborted())
}

func TestRequireTracePermission_EndpointOnlyScopes(t *testing.T) {
	deps := &Dependencies{Storage: permMock(map[string]bool{permEndpointTraceRead: true})}
	c, _ := traceCtx("user-1", "ws1")

	requireTracePermission(deps)(c)

	assert.False(t, c.IsAborted())
	assert.Equal(t, "endpoint", c.GetString(traceEndpointTypeKey))
}

func TestRequireTracePermission_ExternalOnlyScopes(t *testing.T) {
	deps := &Dependencies{Storage: permMock(map[string]bool{permExternalEndpointTraceRead: true})}
	c, _ := traceCtx("user-1", "ws1")

	requireTracePermission(deps)(c)

	assert.False(t, c.IsAborted())
	assert.Equal(t, "external-endpoint", c.GetString(traceEndpointTypeKey))
}

func TestRequireTracePermission_BothGrantedUnrestricted(t *testing.T) {
	deps := &Dependencies{Storage: permMock(map[string]bool{
		permEndpointTraceRead:         true,
		permExternalEndpointTraceRead: true,
	})}
	c, _ := traceCtx("user-1", "ws1")

	requireTracePermission(deps)(c)

	assert.False(t, c.IsAborted())
	// No single-type restriction recorded => handlers see both endpoint types.
	assert.Equal(t, "", c.GetString(traceEndpointTypeKey))
}

// scopedPermMock returns a MockStorage whose has_permission resolves via the
// given decision function, keyed on (workspace, permission). A nil workspace
// param (the global check) is passed through as "".
func scopedPermMock(decide func(workspace, perm string) bool) *mocks.MockStorage {
	m := new(mocks.MockStorage)
	m.On("CallDatabaseFunction", "has_permission", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			params := args.Get(1).(map[string]interface{})
			perm, _ := params["required_permission"].(string)
			ws, _ := params["workspace"].(string) // nil (global) → ""
			out := args.Get(2).(*bool)
			*out = decide(ws, perm)
		}).Return(nil)

	return m
}

// mockWorkspaces wires ListWorkspace to return workspaces with the given names.
func mockWorkspaces(m *mocks.MockStorage, names ...string) {
	wss := make([]v1.Workspace, 0, len(names))
	for _, n := range names {
		wss = append(wss, v1.Workspace{Metadata: &v1.Metadata{Name: n}})
	}

	m.On("ListWorkspace", mock.Anything).Return(wss, nil)
}

func TestRequireTracePermission_AllWorkspaces_GlobalBothUnrestricted(t *testing.T) {
	// A global grant on both permissions skips workspace enumeration and imposes
	// no workspace constraint (scope "*").
	store := scopedPermMock(func(ws, _ string) bool { return ws == "" })
	deps := &Dependencies{Storage: store}
	c, _ := traceCtx("user-1", "_all_")

	requireTracePermission(deps)(c)

	assert.False(t, c.IsAborted())
	assert.Equal(t, "*", c.GetString(traceScopeFilterKey))
	store.AssertNotCalled(t, "ListWorkspace", mock.Anything)
}

func TestRequireTracePermission_AllWorkspaces_NoGrantsForbidden(t *testing.T) {
	// No global grant and no per-workspace grant => 403, not an empty result.
	store := scopedPermMock(func(_, _ string) bool { return false })
	mockWorkspaces(store, "ws1", "ws2")
	deps := &Dependencies{Storage: store}
	c, w := traceCtx("user-1", "_all_")

	requireTracePermission(deps)(c)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.True(t, c.IsAborted())
}

func TestRequireTracePermission_AllWorkspaces_ScopedBuildsFilter(t *testing.T) {
	// No global grant; the caller holds both permissions in ws1, endpoint-only in
	// ws2, and nothing in ws3. The scope OR-expression must reflect exactly that.
	store := scopedPermMock(func(ws, perm string) bool {
		switch ws {
		case "ws1":
			return true
		case "ws2":
			return perm == permEndpointTraceRead
		default: // "" (global) and ws3
			return false
		}
	})
	mockWorkspaces(store, "ws1", "ws2", "ws3")
	deps := &Dependencies{Storage: store}
	c, _ := traceCtx("user-1", "_all_")

	requireTracePermission(deps)(c)

	assert.False(t, c.IsAborted())
	assert.Equal(t,
		`((workspace:="ws1") OR (workspace:="ws2" endpoint_type:="endpoint"))`,
		c.GetString(traceScopeFilterKey),
	)
}

func TestRequireTracePermission_AllWorkspaces_GlobalEndpointOnly(t *testing.T) {
	// A global grant on only the endpoint permission yields an unscoped
	// endpoint_type clause (so retained traces of deleted workspaces stay
	// visible), OR'd with any workspace-scoped external grant (here ws2). It must
	// not narrow the global endpoint permission to current workspaces.
	store := scopedPermMock(func(ws, perm string) bool {
		if perm == permEndpointTraceRead {
			return true // global endpoint grant
		}

		return ws == "ws2" // workspace-scoped external grant
	})
	mockWorkspaces(store, "ws1", "ws2")
	deps := &Dependencies{Storage: store}
	c, _ := traceCtx("user-1", "_all_")

	requireTracePermission(deps)(c)

	assert.False(t, c.IsAborted())
	assert.Equal(t,
		`(endpoint_type:="endpoint" OR (workspace:="ws2" endpoint_type:="external-endpoint"))`,
		c.GetString(traceScopeFilterKey),
	)
}

func TestRequireTracePermission_AllWorkspaces_GlobalExternalOnly(t *testing.T) {
	// Symmetric to the endpoint-only case: a global external grant yields an
	// unscoped external endpoint_type clause, OR'd with any workspace-scoped
	// endpoint grant (here ws1).
	store := scopedPermMock(func(ws, perm string) bool {
		if perm == permExternalEndpointTraceRead {
			return true // global external grant
		}

		return ws == "ws1" // workspace-scoped endpoint grant
	})
	mockWorkspaces(store, "ws1", "ws2")
	deps := &Dependencies{Storage: store}
	c, _ := traceCtx("user-1", "_all_")

	requireTracePermission(deps)(c)

	assert.False(t, c.IsAborted())
	assert.Equal(t,
		`(endpoint_type:="external-endpoint" OR (workspace:="ws1" endpoint_type:="endpoint"))`,
		c.GetString(traceScopeFilterKey),
	)
}

func TestRequireTracePermission_AllWorkspaces_GlobalEndpointOnlyNoWorkspaceGrants(t *testing.T) {
	// Global endpoint permission with no extra external grants: a single unscoped
	// endpoint_type clause, with no per-workspace clauses appended.
	store := scopedPermMock(func(_, perm string) bool {
		return perm == permEndpointTraceRead
	})
	mockWorkspaces(store, "ws1", "ws2")
	deps := &Dependencies{Storage: store}
	c, _ := traceCtx("user-1", "_all_")

	requireTracePermission(deps)(c)

	assert.False(t, c.IsAborted())
	assert.Equal(t, `(endpoint_type:="endpoint")`, c.GetString(traceScopeFilterKey))
}

func TestTraceScopeClause_ConcreteWorkspace(t *testing.T) {
	// Concrete workspace, both endpoint types: bare workspace clause.
	c, _ := traceCtx("user-1", "ws1")
	assert.Equal(t, `workspace:="ws1"`, traceScopeClause(c))

	// Concrete workspace restricted to one endpoint type folds the type in.
	c.Set(traceEndpointTypeKey, "endpoint")
	assert.Equal(t, `workspace:="ws1" endpoint_type:="endpoint"`, traceScopeClause(c))
}

func TestTraceScopeClause_AllWorkspacesUsesPrecomputedFilter(t *testing.T) {
	c, _ := traceCtx("user-1", "_all_")
	c.Set(traceScopeFilterKey, `((workspace:="ws1"))`)
	assert.Equal(t, `((workspace:="ws1"))`, traceScopeClause(c))
}

func TestAITraceHandlers_StoreNotConfigured(t *testing.T) {
	// With no --ai-trace-store-url, every trace route returns 503 before any
	// storage/VictoriaLogs interaction.
	deps := &Dependencies{}

	handlers := map[string]gin.HandlerFunc{
		"list":      handleListAITraces(deps),
		"stats":     handleAITraceStats(deps),
		"get":       handleGetAITrace(deps),
		"key-stats": handleAITraceKeyStats(deps),
	}

	for name, h := range handlers {
		c, w := traceCtx("user-1", "ws1")
		h(c)
		assert.Equalf(t, http.StatusServiceUnavailable, w.Code, "handler %s", name)
		assert.Containsf(t, w.Body.String(), "not configured", "handler %s", name)
	}
}

func TestHandleListAITraces_IncludeBodyProjection(t *testing.T) {
	// The list projection omits the large body columns by default and includes
	// them only when the caller passes ?include_body=true.
	cases := []struct {
		name        string
		rawQuery    string
		wantInQuery bool
	}{
		{"default omits bodies", "", false},
		{"include_body adds bodies", "include_body=true", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotQuery string

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotQuery = r.URL.Query().Get("query")
				w.Header().Set("Content-Type", "application/x-ndjson")
			}))
			defer server.Close()

			deps := &Dependencies{AITraceStoreURL: server.URL, HTTPClient: &util.DefaultHTTPClient{}}

			c, w := traceCtx("user-1", "ws1")
			c.Request = httptest.NewRequest("GET", "/?"+tc.rawQuery, nil)

			handleListAITraces(deps)(c)

			require.Equal(t, http.StatusOK, w.Code)
			assert.Contains(t, gotQuery, "| fields ")

			hasBodies := strings.Contains(gotQuery, "request_body, response_body")
			assert.Equal(t, tc.wantInQuery, hasBodies, "query: %s", gotQuery)
		})
	}
}

func TestHandleAITraceKeyStats_DecodesNDJSON(t *testing.T) {
	// VictoriaLogs returns one NDJSON row per api_key_id (all values as strings),
	// plus an untagged row (empty api_key_id) that must be dropped.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(
			`{"api_key_id":"k1","requests":"3010","tokens":"1240000","avg_duration_ms":"8591.27","success":"3005"}` + "\n" +
				`{"api_key_id":"k2","requests":"52","tokens":"36805","avg_duration_ms":"2308.1","success":"49"}` + "\n" +
				`{"api_key_id":"","requests":"7","tokens":"100","avg_duration_ms":"1","success":"7"}` + "\n"))
	}))
	defer server.Close()

	deps := &Dependencies{AITraceStoreURL: server.URL, HTTPClient: &util.DefaultHTTPClient{}}
	c, w := traceCtx("user-1", "ws1")

	handleAITraceKeyStats(deps)(c)

	require.Equal(t, http.StatusOK, w.Code)
	var resp AITraceKeyStatsResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, 24, resp.WindowHours) // default window
	require.Len(t, resp.Keys, 2)          // untagged row dropped
	assert.Equal(t, "k1", resp.Keys[0].APIKeyID)
	assert.Equal(t, int64(3010), resp.Keys[0].Requests)
	assert.Equal(t, int64(1240000), resp.Keys[0].Tokens)
	assert.Equal(t, int64(3005), resp.Keys[0].Success)
	assert.InDelta(t, 8591.27, resp.Keys[0].AvgDurationMs, 0.01)
	assert.Equal(t, "k2", resp.Keys[1].APIKeyID)
	assert.Equal(t, int64(52), resp.Keys[1].Requests)
}

func TestHandleListAITraces_IncludeBodyClampsLimit(t *testing.T) {
	// Body-carrying pages are clamped to includeBodyMaxLimit; metadata-only
	// pages keep the requested size.
	cases := []struct {
		name      string
		rawQuery  string
		wantLimit string
	}{
		{"metadata keeps 500", "limit=500", "| limit 500"},
		{"body clamps to 50", "limit=500&include_body=true", "| limit 50"},
		{"body below cap untouched", "limit=10&include_body=true", "| limit 10"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotQuery string

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotQuery = r.URL.Query().Get("query")
				w.Header().Set("Content-Type", "application/x-ndjson")
			}))
			defer server.Close()

			deps := &Dependencies{AITraceStoreURL: server.URL, HTTPClient: &util.DefaultHTTPClient{}}

			c, w := traceCtx("user-1", "ws1")
			c.Request = httptest.NewRequest("GET", "/?"+tc.rawQuery, nil)

			handleListAITraces(deps)(c)

			require.Equal(t, http.StatusOK, w.Code)
			assert.Contains(t, gotQuery, tc.wantLimit, "query: %s", gotQuery)
		})
	}
}
