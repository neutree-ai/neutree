package logs

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// AITrace is one inference trace record returned to the SPA.
//
// Mirrors the shape Vector pushes to VictoriaLogs, with response_status
// normalised to int for the UI's filtering convenience. The list endpoint
// leaves RequestBody/ResponseBody empty — they are large and unused by the
// list view; the detail endpoint populates them for a single record.
type AITrace struct {
	RequestID        string `json:"request_id"`
	Time             string `json:"time"`
	Workspace        string `json:"workspace"`
	EndpointType     string `json:"endpoint_type"`
	EndpointName     string `json:"endpoint_name"`
	APIKeyID         string `json:"api_key_id,omitempty"`
	RequestURI       string `json:"request_uri,omitempty"`
	RequestModel     string `json:"request_model,omitempty"`
	ResponseModel    string `json:"response_model,omitempty"`
	ResponseStatus   int    `json:"response_status"`
	PromptTokens     *int   `json:"prompt_tokens,omitempty"`
	CompletionTokens *int   `json:"completion_tokens,omitempty"`
	TotalTokens      *int   `json:"total_tokens,omitempty"`
	FinishReason     string `json:"finish_reason,omitempty"`
	Stream           bool   `json:"stream"`
	UserAgent        string `json:"user_agent,omitempty"`
	DurationMs       *int   `json:"duration_ms,omitempty"`
	RequestBody      string `json:"request_body,omitempty"`
	ResponseBody     string `json:"response_body,omitempty"`
}

// listProjection is the LogsQL `fields` projection for the list query: every
// metadata column the list view renders, deliberately excluding the large
// request_body / response_body fields so list responses stay small.
const listProjection = "_time, request_id, workspace, endpoint_type, " +
	"endpoint_name, api_key_id, request_uri, request_model, response_model, " +
	"response_status, prompt_tokens, completion_tokens, total_tokens, " +
	"finish_reason, stream, user_agent, duration_ms"

// AITraceListResponse is the wire format for GET /api/v1/ai-traces/:workspace.
//
// NextBefore is the cursor for the next page: passing it as ?before=<ts> on
// the next call returns the next batch. Empty when there are no older records.
type AITraceListResponse struct {
	Items      []AITrace `json:"items"`
	NextBefore string    `json:"next_before,omitempty"`
}

// AITraceDayCount is one day's request count in the stats response.
type AITraceDayCount struct {
	Date  string `json:"date"` // YYYY-MM-DD, UTC day bucket
	Count int    `json:"count"`
}

// AITraceStatsResponse is the wire format for
// GET /api/v1/ai-traces/:workspace/stats — per-day request counts powering
// the activity bar chart at the top of the SPA's trace list.
type AITraceStatsResponse struct {
	Days []AITraceDayCount `json:"days"`
}

// RegisterAITraceRoutes mounts the AI inference trace endpoints.
func RegisterAITraceRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	traces := group.Group("/ai-traces/:workspace")
	traces.Use(middlewares...)
	traces.Use(requireTracePermission(deps))

	traces.GET("", handleListAITraces(deps))
	traces.GET("/stats", handleAITraceStats(deps))
	traces.GET("/:request_id", handleGetAITrace(deps))
}

const (
	permEndpointTraceRead         = "endpoint:trace-read"
	permExternalEndpointTraceRead = "external_endpoint:trace-read"
	// traceEndpointTypeKey holds, in the gin context, the single endpoint_type a
	// caller is restricted to ("endpoint" or "external-endpoint"); empty = both.
	traceEndpointTypeKey = "trace_endpoint_type"
	// traceScopeFilterKey holds, in the gin context, a ready-to-use LogsQL
	// boolean expression constraining results to the workspaces (and per-workspace
	// endpoint types) the caller may read. Set only on the "all workspaces" path;
	// empty for a concrete workspace, where traceScopeClause builds the clause
	// from the path param instead.
	traceScopeFilterKey = "trace_scope_filter"
	// allWorkspacesSentinel mirrors the SPA's ALL_WORKSPACES constant
	// (foundation/hooks/use-workspace.ts). It is a UI-only value, never a real
	// workspace name; selecting it asks for traces aggregated across every
	// workspace the caller is permitted to read.
	allWorkspacesSentinel = "_all_"
)

// requireTracePermission gates the ai-trace routes on endpoint:trace-read OR
// external_endpoint:trace-read for the workspace. A caller holding only one of
// the two is restricted to that endpoint type (recorded in the context for the
// handlers to scope their LogsQL query). Both checks pass the workspace to
// has_permission, which the enterprise edition overrides to be workspace-aware.
func requireTracePermission(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetString("user_id")
		if userID == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "user not authenticated"})
			c.Abort()

			return
		}

		workspace := c.Param("workspace")
		if workspace == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "workspace parameter is required"})
			c.Abort()

			return
		}

		// "All workspaces" is a cross-workspace aggregate, not a real workspace.
		// It needs its own permission resolution: the caller may read traces in
		// only a subset of workspaces, so we compute a scope filter rather than a
		// single yes/no check against a (non-existent) workspace named "_all_".
		if workspace == allWorkspacesSentinel {
			resolveAllWorkspacesTraceScope(c, deps, userID)

			return
		}

		canEndpoint, err := middleware.CheckWorkspacePermission(deps.Storage, userID, workspace, permEndpointTraceRead)
		if err != nil {
			tracePermError(c, userID, err)

			return
		}

		canExternal, err := middleware.CheckWorkspacePermission(deps.Storage, userID, workspace, permExternalEndpointTraceRead)
		if err != nil {
			tracePermError(c, userID, err)

			return
		}

		if !canEndpoint && !canExternal {
			c.JSON(http.StatusForbidden, gin.H{
				"error":    "insufficient permissions",
				"required": permEndpointTraceRead + " or " + permExternalEndpointTraceRead,
			})
			c.Abort()

			return
		}

		// Holding only one permission restricts the caller to that endpoint type.
		if canEndpoint && !canExternal {
			c.Set(traceEndpointTypeKey, "endpoint")
		} else if canExternal && !canEndpoint {
			c.Set(traceEndpointTypeKey, "external-endpoint")
		}

		c.Next()
	}
}

func tracePermError(c *gin.Context, userID string, err error) {
	klog.Errorf("ai-trace: permission check failed for user %s: %v", userID, err)
	c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to check permissions"})
	c.Abort()
}

// resolveAllWorkspacesTraceScope handles the "_all_" aggregate: it computes the
// LogsQL scope filter restricting results to the workspaces (and per-workspace
// endpoint types) the caller is permitted to read, stores it under
// traceScopeFilterKey, and continues — or aborts 403 when the caller may read
// no workspace at all.
func resolveAllWorkspacesTraceScope(c *gin.Context, deps *Dependencies, userID string) {
	// Fast path: a global grant on both trace permissions lets the caller read
	// every workspace's traces — including any belonging to since-deleted
	// workspaces — so we impose no workspace constraint at all.
	globalEndpoint, err := middleware.CheckWorkspacePermission(deps.Storage, userID, "", permEndpointTraceRead)
	if err != nil {
		tracePermError(c, userID, err)

		return
	}

	globalExternal, err := middleware.CheckWorkspacePermission(deps.Storage, userID, "", permExternalEndpointTraceRead)
	if err != nil {
		tracePermError(c, userID, err)

		return
	}

	if globalEndpoint && globalExternal {
		c.Set(traceScopeFilterKey, "*")
		c.Next()

		return
	}

	clauses := make([]string, 0)

	// A trace permission held globally grants that endpoint type across *every*
	// workspace — including retained traces of since-deleted workspaces — so emit
	// an unscoped endpoint_type clause instead of enumerating current workspaces.
	// This keeps "_all_" consistent with the both-global "*" fast path above:
	// whether a caller holds one or both permissions globally, deleted-workspace
	// traces for the permitted endpoint type(s) stay visible.
	if globalEndpoint {
		clauses = append(clauses, fmt.Sprintf("endpoint_type:=%s", logsQLQuoteValue("endpoint")))
	}

	if globalExternal {
		clauses = append(clauses, fmt.Sprintf("endpoint_type:=%s", logsQLQuoteValue("external-endpoint")))
	}

	// Enumerate per-workspace grants only for the permission(s) NOT held globally;
	// the global ones are already covered by the unscoped clauses above. Each
	// workspace the caller can read contributes an OR clause, narrowed to a single
	// endpoint_type where only one permission is held there.
	if !globalEndpoint || !globalExternal {
		workspaces, err := deps.Storage.ListWorkspace(storage.ListOption{})
		if err != nil {
			tracePermError(c, userID, err)

			return
		}

		for i := range workspaces {
			name := workspaces[i].GetName()
			if name == "" {
				continue
			}

			canEndpoint := false
			if !globalEndpoint {
				canEndpoint, err = middleware.CheckWorkspacePermission(deps.Storage, userID, name, permEndpointTraceRead)
				if err != nil {
					tracePermError(c, userID, err)

					return
				}
			}

			canExternal := false
			if !globalExternal {
				canExternal, err = middleware.CheckWorkspacePermission(deps.Storage, userID, name, permExternalEndpointTraceRead)
				if err != nil {
					tracePermError(c, userID, err)

					return
				}
			}

			if clause := workspaceScopeClause(name, canEndpoint, canExternal); clause != "" {
				clauses = append(clauses, clause)
			}
		}
	}

	if len(clauses) == 0 {
		c.JSON(http.StatusForbidden, gin.H{
			"error":    "insufficient permissions",
			"required": permEndpointTraceRead + " or " + permExternalEndpointTraceRead,
		})
		c.Abort()

		return
	}

	c.Set(traceScopeFilterKey, "("+strings.Join(clauses, " OR ")+")")
	c.Next()
}

// workspaceScopeClause builds the LogsQL sub-clause for one workspace given which
// trace permissions the caller holds there. A caller holding both permissions
// sees the whole workspace; holding only one is narrowed to that endpoint type.
// Returns "" when the caller may read neither endpoint type.
func workspaceScopeClause(workspace string, canEndpoint, canExternal bool) string {
	switch {
	case canEndpoint && canExternal:
		return fmt.Sprintf("(workspace:=%s)", logsQLQuoteValue(workspace))
	case canEndpoint:
		return fmt.Sprintf("(workspace:=%s endpoint_type:=%s)",
			logsQLQuoteValue(workspace), logsQLQuoteValue("endpoint"))
	case canExternal:
		return fmt.Sprintf("(workspace:=%s endpoint_type:=%s)",
			logsQLQuoteValue(workspace), logsQLQuoteValue("external-endpoint"))
	default:
		return ""
	}
}

// traceScopeClause returns the LogsQL expression constraining a query to the
// workspace(s) and endpoint type(s) the caller may read. For the "_all_"
// aggregate it is the scope filter precomputed by the permission middleware; for
// a concrete workspace it is `workspace:="<ws>"` plus any single-endpoint-type
// restriction the caller is limited to.
func traceScopeClause(c *gin.Context) string {
	if scope := c.GetString(traceScopeFilterKey); scope != "" {
		return scope
	}

	clause := fmt.Sprintf("workspace:=%s", logsQLQuoteValue(c.Param("workspace")))
	if et := traceEndpointTypeFilter(c); et != "" {
		clause += " " + et
	}

	return clause
}

// traceEndpointTypeFilter returns a LogsQL clause restricting results to the
// endpoint_type the caller is permitted to see, or "" when unrestricted.
func traceEndpointTypeFilter(c *gin.Context) string {
	et := c.GetString(traceEndpointTypeKey)
	if et == "" {
		return ""
	}

	return fmt.Sprintf("endpoint_type:=%s", logsQLQuoteValue(et))
}

func handleListAITraces(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if deps.AITraceStoreURL == "" {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "ai trace store is not configured",
			})

			return
		}

		limit := 50

		if v := c.Query("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
				limit = n
			}
		}

		// Leading clause scopes the query to the workspace(s) the caller may read
		// (a concrete workspace, or the cross-workspace "_all_" aggregate).
		queryParts := []string{traceScopeClause(c)}

		if endpoint := strings.TrimSpace(c.Query("endpoint_name")); endpoint != "" {
			queryParts = append(queryParts, fmt.Sprintf("endpoint_name:=%s", logsQLQuoteValue(endpoint)))
		}

		if endpointType := strings.TrimSpace(c.Query("endpoint_type")); endpointType != "" {
			queryParts = append(queryParts, fmt.Sprintf("endpoint_type:=%s", logsQLQuoteValue(endpointType)))
		}

		if status := strings.TrimSpace(c.Query("status")); status != "" {
			queryParts = append(queryParts, fmt.Sprintf("response_status:=%s", logsQLQuoteValue(status)))
		}

		if apiKeyID := strings.TrimSpace(c.Query("api_key_id")); apiKeyID != "" {
			queryParts = append(queryParts, fmt.Sprintf("api_key_id:=%s", logsQLQuoteValue(apiKeyID)))
		}

		if fr := strings.TrimSpace(c.Query("finish_reason")); fr != "" {
			queryParts = append(queryParts, fmt.Sprintf("finish_reason:=%s", logsQLQuoteValue(fr)))
		}

		if model := strings.TrimSpace(c.Query("model")); model != "" {
			// model can match either request or response model
			queryParts = append(queryParts, fmt.Sprintf(
				"(request_model:=%s OR response_model:=%s)",
				logsQLQuoteValue(model), logsQLQuoteValue(model),
			))
		}

		// The list view never renders request/response bodies — project them
		// out so VictoriaLogs returns only the small metadata columns.
		query := strings.Join(queryParts, " ") +
			" | sort by (_time) desc | limit " + strconv.Itoa(limit) +
			" | fields " + listProjection

		params := url.Values{}

		if start := strings.TrimSpace(c.Query("start")); start != "" {
			params.Set("start", start)
		}
		// `before` is the inclusive upper bound from the previous page's last
		// record's timestamp. To page strictly older we pass it as end.
		if before := strings.TrimSpace(c.Query("before")); before != "" {
			params.Set("end", before)
		} else if end := strings.TrimSpace(c.Query("end")); end != "" {
			params.Set("end", end)
		}

		items, err := queryAITraces(deps, query, params)
		if err != nil {
			klog.Errorf("ai-trace: list: %v", err)
			c.JSON(http.StatusBadGateway, gin.H{
				"error": "failed to query trace store",
			})

			return
		}

		var nextBefore string
		if len(items) == limit {
			nextBefore = items[len(items)-1].Time
		}

		c.JSON(http.StatusOK, AITraceListResponse{
			Items:      items,
			NextBefore: nextBefore,
		})
	}
}

// handleGetAITrace returns a single trace — including the full request and
// response bodies — looked up by request id. The list endpoint omits bodies,
// so the SPA's detail drawer fetches them lazily through this route.
func handleGetAITrace(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if deps.AITraceStoreURL == "" {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "ai trace store is not configured",
			})

			return
		}

		requestID := strings.TrimSpace(c.Param("request_id"))
		if requestID == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "request_id is required",
			})

			return
		}

		// The scope clause confines the lookup to workspaces/endpoint types the
		// caller may read, so an "_all_" detail fetch cannot read a trace the
		// caller has no permission for.
		query := fmt.Sprintf(
			"%s request_id:=%s | sort by (_time) desc | limit 1",
			traceScopeClause(c), logsQLQuoteValue(requestID),
		)

		items, err := queryAITraces(deps, query, url.Values{})
		if err != nil {
			klog.Errorf("ai-trace: detail: %v", err)
			c.JSON(http.StatusBadGateway, gin.H{
				"error": "failed to query trace store",
			})

			return
		}

		if len(items) == 0 {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "trace not found",
			})

			return
		}

		c.JSON(http.StatusOK, items[0])
	}
}

// maxStatsDays caps the number of UTC day buckets the stats endpoint returns,
// bounding both the trailing-`days` fallback and an explicit [start, end] window.
const maxStatsDays = 90

// handleAITraceStats returns per-day request counts for the requested window,
// powering the activity bar chart at the top of the SPA's trace list.
func handleAITraceStats(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if deps.AITraceStoreURL == "" {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "ai trace store is not configured",
			})

			return
		}

		startDay, endDay, ok := statsWindow(c)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "invalid start/end parameter",
			})

			return
		}

		query := fmt.Sprintf(
			"%s | stats by (_time:1d) count() total",
			traceScopeClause(c),
		)

		params := url.Values{}
		params.Set("start", startDay.Format(time.RFC3339))
		// `end` is pushed to the start of the day after endDay so the final
		// (possibly partial) day is always fully covered, regardless of whether
		// VictoriaLogs treats the bound as inclusive or exclusive.
		params.Set("end", endDay.AddDate(0, 0, 1).Format(time.RFC3339))

		counts, err := queryAITraceDayCounts(deps, query, params)
		if err != nil {
			klog.Errorf("ai-trace: stats: %v", err)
			c.JSON(http.StatusBadGateway, gin.H{
				"error": "failed to query trace store",
			})

			return
		}

		out := make([]AITraceDayCount, 0)

		for d := startDay; !d.After(endDay); d = d.AddDate(0, 0, 1) {
			date := d.Format("2006-01-02")
			out = append(out, AITraceDayCount{Date: date, Count: counts[date]})
		}

		c.JSON(http.StatusOK, AITraceStatsResponse{Days: out})
	}
}

// statsWindow resolves the inclusive UTC day-bucket range for the stats query.
//
// `start` and `end` (RFC3339, the same params the list endpoint accepts) are
// independent, optional bounds:
//   - `end` defaults to the current (partial) UTC day when omitted.
//   - `start` present anchors an explicit [start, end] window.
//   - `start` absent falls back to a trailing `days` window (default 7) ending
//     at `end`. This means an `end`-only request is a valid trailing window
//     rather than a 400 — only a malformed start/end is rejected.
//
// Both bounds are truncated to their UTC day and the span is clamped to
// maxStatsDays buckets. ok is false only when start/end is malformed RFC3339 or
// end precedes start.
func statsWindow(c *gin.Context) (startDay, endDay time.Time, ok bool) {
	now := time.Now().UTC()

	startStr := strings.TrimSpace(c.Query("start"))
	endStr := strings.TrimSpace(c.Query("end"))

	// `end` defaults to now; an explicit value overrides it. Day buckets are
	// aligned to UTC midnight.
	end := now

	if endStr != "" {
		parsed, err := time.Parse(time.RFC3339, endStr)
		if err != nil {
			return time.Time{}, time.Time{}, false
		}

		end = parsed
	}

	endDay = end.UTC().Truncate(24 * time.Hour)

	if startStr != "" {
		// Explicit window: [start, end].
		start, err := time.Parse(time.RFC3339, startStr)
		if err != nil {
			return time.Time{}, time.Time{}, false
		}

		startDay = start.UTC().Truncate(24 * time.Hour)

		if endDay.Before(startDay) {
			return time.Time{}, time.Time{}, false
		}

		// Clamp to maxStatsDays buckets, keeping the most recent end.
		if minStart := endDay.AddDate(0, 0, -(maxStatsDays - 1)); startDay.Before(minStart) {
			startDay = minStart
		}

		return startDay, endDay, true
	}

	// No `start`: trailing `days` window (default 7) ending at `endDay`.
	days := 7

	if v := c.Query("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= maxStatsDays {
			days = n
		}
	}

	startDay = endDay.AddDate(0, 0, -(days - 1))

	return startDay, endDay, true
}

// queryAITraceDayCounts runs a `stats by (_time:1d)` LogsQL query and returns
// a map of UTC date (YYYY-MM-DD) to request count.
func queryAITraceDayCounts(deps *Dependencies, query string, params url.Values) (map[string]int, error) {
	params.Set("query", query)
	reqURL := strings.TrimRight(deps.AITraceStoreURL, "/") + "/select/logsql/query?" + params.Encode()

	resp, err := deps.HTTPClient.Get(reqURL)
	if err != nil {
		return nil, fmt.Errorf("query victorialogs: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("victorialogs returned status %d", resp.StatusCode)
	}

	counts := make(map[string]int)
	scanner := bufio.NewScanner(resp.Body)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var r struct {
			Time  string `json:"_time"`
			Total string `json:"total"`
		}

		if err := json.Unmarshal(line, &r); err != nil {
			continue
		}

		// `_time` is an RFC3339 day bucket; key by the date component.
		date := r.Time
		if len(date) >= 10 {
			date = date[:10]
		}

		if n, err := strconv.Atoi(r.Total); err == nil {
			counts[date] = n
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan victorialogs response: %w", err)
	}

	return counts, nil
}

// queryAITraces runs a LogsQL query against VictoriaLogs and decodes the
// NDJSON response into AITrace records.
func queryAITraces(deps *Dependencies, query string, params url.Values) ([]AITrace, error) {
	params.Set("query", query)
	reqURL := strings.TrimRight(deps.AITraceStoreURL, "/") + "/select/logsql/query?" + params.Encode()

	resp, err := deps.HTTPClient.Get(reqURL)
	if err != nil {
		return nil, fmt.Errorf("query victorialogs: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("victorialogs returned status %d", resp.StatusCode)
	}

	items := make([]AITrace, 0, 64)
	scanner := bufio.NewScanner(resp.Body)
	// A detail record can be large because it embeds the full request and
	// response bodies.
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		item, ok := decodeVLRecord(line)
		if !ok {
			continue
		}

		items = append(items, item)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan victorialogs response: %w", err)
	}

	return items, nil
}

// vlRecord matches the shape Vector writes to VictoriaLogs.
// All values come back as strings; we coerce types we care about.
type vlRecord struct {
	Time             string `json:"_time"`
	Stream           string `json:"_stream,omitempty"`
	RequestID        string `json:"request_id"`
	Workspace        string `json:"workspace"`
	EndpointType     string `json:"endpoint_type"`
	EndpointName     string `json:"endpoint_name"`
	APIKeyID         string `json:"api_key_id"`
	RequestURI       string `json:"request_uri"`
	RequestModel     string `json:"request_model"`
	ResponseModel    string `json:"response_model"`
	ResponseStatus   string `json:"response_status"`
	PromptTokens     string `json:"prompt_tokens"`
	CompletionTokens string `json:"completion_tokens"`
	TotalTokens      string `json:"total_tokens"`
	FinishReason     string `json:"finish_reason"`
	IsStream         string `json:"stream"`
	UserAgent        string `json:"user_agent"`
	DurationMs       string `json:"duration_ms"`
	RequestBody      string `json:"request_body"`
	ResponseBody     string `json:"response_body"`
}

func decodeVLRecord(line []byte) (AITrace, bool) {
	var r vlRecord
	if err := json.Unmarshal(line, &r); err != nil {
		return AITrace{}, false
	}

	t := AITrace{
		RequestID:     r.RequestID,
		Time:          r.Time,
		Workspace:     r.Workspace,
		EndpointType:  r.EndpointType,
		EndpointName:  r.EndpointName,
		APIKeyID:      r.APIKeyID,
		RequestURI:    r.RequestURI,
		RequestModel:  r.RequestModel,
		ResponseModel: r.ResponseModel,
		FinishReason:  r.FinishReason,
		Stream:        r.IsStream == "true",
		UserAgent:     r.UserAgent,
		RequestBody:   r.RequestBody,
		ResponseBody:  r.ResponseBody,
	}
	if r.Time == "" {
		t.Time = time.Now().UTC().Format(time.RFC3339Nano)
	}

	if n, err := strconv.Atoi(r.ResponseStatus); err == nil {
		t.ResponseStatus = n
	}

	if r.PromptTokens != "" {
		if n, err := strconv.Atoi(r.PromptTokens); err == nil {
			t.PromptTokens = &n
		}
	}

	if r.CompletionTokens != "" {
		if n, err := strconv.Atoi(r.CompletionTokens); err == nil {
			t.CompletionTokens = &n
		}
	}

	if r.TotalTokens != "" {
		if n, err := strconv.Atoi(r.TotalTokens); err == nil {
			t.TotalTokens = &n
		}
	}

	if r.DurationMs != "" {
		// `latencies.request` arrives as a float-formatted string from Vector.
		if f, err := strconv.ParseFloat(r.DurationMs, 64); err == nil {
			n := int(f)
			t.DurationMs = &n
		}
	}

	return t, true
}

// logsQLQuoteValue wraps a string literal for an exact-match LogsQL filter
// (`field:=value`). VL accepts double-quoted strings; inner quotes/backslashes
// are escaped. Empty input is permitted by the caller and produces `""`.
func logsQLQuoteValue(v string) string {
	var b strings.Builder
	b.Grow(len(v) + 2)
	b.WriteByte('"')

	for _, r := range v {
		switch r {
		case '\\', '"':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString("\\n")
		default:
			b.WriteRune(r)
		}
	}

	b.WriteByte('"')

	return b.String()
}
