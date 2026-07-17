package logs

import (
	"fmt"
	"net/http"
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

	// BodyTruncated marks a record whose bodies exceeded the ingestion cap and
	// were cut off by Vector; the stored bodies are a prefix of the originals.
	BodyTruncated bool `json:"body_truncated,omitempty"`
	// BodyIncomplete marks a chunked record for which some body chunks could
	// not be read back (partial ingestion loss); the returned bodies hold the
	// longest decodable prefix.
	BodyIncomplete bool `json:"body_incomplete,omitempty"`

	// Chunked-body storage metadata, private to the trace store: whether this
	// record's bodies live in companion chunk records, and how many each body
	// was split into. Never serialized; the API always returns whole bodies.
	bodyChunked    bool
	requestChunks  int
	responseChunks int
}

// stringTrue is the literal a boolean-valued string (a query flag, or a
// stringified VictoriaLogs field) must equal to be considered on.
const stringTrue = "true"

// includeBodyMaxLimit caps the page size of body-carrying list queries
// (?include_body=true); metadata-only pages may go up to 500.
const includeBodyMaxLimit = 50

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

// AITraceKeyStat is one API key's aggregated traffic over the requested window.
// Powers the API-key list ranking overview and the detail "request performance"
// card. SuccessRate is derived client-side as Success/Requests.
type AITraceKeyStat struct {
	APIKeyID      string  `json:"api_key_id"`
	Requests      int64   `json:"requests"`
	Tokens        int64   `json:"tokens"`
	Success       int64   `json:"success"`
	AvgDurationMs float64 `json:"avg_duration_ms"`
}

// AITraceKeyStatsResponse is the wire format for
// GET /api/v1/ai-traces/:workspace/key-stats — per-API-key request count, token
// total, success count and average latency over a trailing window (default 24h).
type AITraceKeyStatsResponse struct {
	WindowHours int              `json:"window_hours"`
	Keys        []AITraceKeyStat `json:"keys"`
}

// RegisterAITraceRoutes mounts the AI inference trace endpoints.
func RegisterAITraceRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	traces := group.Group("/ai-traces/:workspace")
	traces.Use(middlewares...)
	traces.Use(requireTracePermission(deps))

	traces.GET("", handleListAITraces(deps))
	traces.GET("/stats", handleAITraceStats(deps))
	traces.GET("/key-stats", handleAITraceKeyStats(deps))
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

// requireTraceStore resolves the trace store, or answers 503 and returns nil
// when no --ai-trace-store-url is configured.
func requireTraceStore(c *gin.Context, deps *Dependencies) *traceStore {
	store := newTraceStore(deps)
	if store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "ai trace store is not configured",
		})
	}

	return store
}

func handleListAITraces(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		store := requireTraceStore(c, deps)
		if store == nil {
			return
		}

		limit := 50

		if v := c.Query("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
				limit = n
			}
		}

		filters := traceFilters{
			EndpointName: strings.TrimSpace(c.Query("endpoint_name")),
			EndpointType: strings.TrimSpace(c.Query("endpoint_type")),
			Status:       strings.TrimSpace(c.Query("status")),
			APIKeyID:     strings.TrimSpace(c.Query("api_key_id")),
			FinishReason: strings.TrimSpace(c.Query("finish_reason")),
			Model:        strings.TrimSpace(c.Query("model")),
		}

		// The list view never renders request/response bodies, so they are
		// omitted by default. Callers exporting the full record opt in via
		// ?include_body=true so bodies come back inline, avoiding a per-record
		// detail lookup.
		includeBody := c.Query("include_body") == stringTrue

		// Body-carrying pages are bounded tighter: with bodies of up to tens
		// of MiB per record, a 500-record page could produce a GiB-scale
		// response. Clamping here is transparent to pagers — nextBefore is
		// derived from the effective limit, so cursoring still walks the full
		// result set in smaller pages.
		if includeBody && limit > includeBodyMaxLimit {
			limit = includeBodyMaxLimit
		}

		window := timeWindow{Start: strings.TrimSpace(c.Query("start"))}
		// `before` is the inclusive upper bound from the previous page's last
		// record's timestamp. To page strictly older we pass it as end.
		if before := strings.TrimSpace(c.Query("before")); before != "" {
			window.End = before
		} else {
			window.End = strings.TrimSpace(c.Query("end"))
		}

		items, err := store.List(traceScopeClause(c), filters, limit, includeBody, window)
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
		store := requireTraceStore(c, deps)
		if store == nil {
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
		item, err := store.Get(traceScopeClause(c), requestID)
		if err != nil {
			klog.Errorf("ai-trace: detail: %v", err)
			c.JSON(http.StatusBadGateway, gin.H{
				"error": "failed to query trace store",
			})

			return
		}

		if item == nil {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "trace not found",
			})

			return
		}

		c.JSON(http.StatusOK, item)
	}
}

// maxStatsDays caps the number of UTC day buckets the stats endpoint returns,
// bounding both the trailing-`days` fallback and an explicit [start, end] window.
const maxStatsDays = 90

// handleAITraceStats returns per-day request counts for the requested window,
// powering the activity bar chart at the top of the SPA's trace list.
func handleAITraceStats(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		store := requireTraceStore(c, deps)
		if store == nil {
			return
		}

		startDay, endDay, ok := statsWindow(c)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "invalid start/end parameter",
			})

			return
		}

		// `end` is pushed to the start of the day after endDay so the final
		// (possibly partial) day is always fully covered, regardless of whether
		// VictoriaLogs treats the bound as inclusive or exclusive.
		counts, err := store.DayCounts(traceScopeClause(c), startDay, endDay.AddDate(0, 0, 1))
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

// maxKeyStatsWindowHours bounds the trailing window the key-stats endpoint will
// aggregate over (30 days), keeping a single LogsQL scan bounded.
const maxKeyStatsWindowHours = 720

// handleAITraceKeyStats returns per-API-key aggregates (request count, tokens,
// success count, average latency) over a trailing window — default 24h. A single
// `stats by (api_key_id)` LogsQL scan covers every key the caller may read, so
// both the list ranking overview and a single key's detail card are served by
// one call (the detail view picks its key out of the result).
func handleAITraceKeyStats(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		store := requireTraceStore(c, deps)
		if store == nil {
			return
		}

		windowHours := 24

		if v := c.Query("window_hours"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= maxKeyStatsWindowHours {
				windowHours = n
			}
		}

		since := time.Now().UTC().Add(-time.Duration(windowHours) * time.Hour)

		keys, err := store.KeyStats(traceScopeClause(c), since)
		if err != nil {
			klog.Errorf("ai-trace: key-stats: %v", err)
			c.JSON(http.StatusBadGateway, gin.H{
				"error": "failed to query trace store",
			})

			return
		}

		c.JSON(http.StatusOK, AITraceKeyStatsResponse{
			WindowHours: windowHours,
			Keys:        keys,
		})
	}
}
