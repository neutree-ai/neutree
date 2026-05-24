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
	RequestBody      string `json:"request_body,omitempty"`
	ResponseBody     string `json:"response_body,omitempty"`
}

// listProjection is the LogsQL `fields` projection for the list query: every
// metadata column the list view renders, deliberately excluding the large
// request_body / response_body fields so list responses stay small.
const listProjection = "_time, request_id, workspace, endpoint_type, " +
	"endpoint_name, api_key_id, request_uri, request_model, response_model, " +
	"response_status, prompt_tokens, completion_tokens, total_tokens"

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
	traces.Use(middleware.RequireWorkspacePermission("workspace:read", middleware.PermissionDependencies{
		Storage: deps.Storage,
	}))

	traces.GET("", handleListAITraces(deps))
	traces.GET("/stats", handleAITraceStats(deps))
	traces.GET("/:request_id", handleGetAITrace(deps))
}

func handleListAITraces(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if deps.AITraceStoreURL == "" {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "ai trace store is not configured",
			})

			return
		}

		workspace := c.Param("workspace")

		limit := 50

		if v := c.Query("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
				limit = n
			}
		}

		queryParts := []string{fmt.Sprintf("workspace:=%s", logsQLQuoteValue(workspace))}

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

		workspace := c.Param("workspace")

		requestID := strings.TrimSpace(c.Param("request_id"))
		if requestID == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "request_id is required",
			})

			return
		}

		query := fmt.Sprintf(
			"workspace:=%s request_id:=%s | sort by (_time) desc | limit 1",
			logsQLQuoteValue(workspace), logsQLQuoteValue(requestID),
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

// handleAITraceStats returns per-day request counts for the trailing window,
// powering the activity bar chart at the top of the SPA's trace list.
func handleAITraceStats(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if deps.AITraceStoreURL == "" {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "ai trace store is not configured",
			})

			return
		}

		workspace := c.Param("workspace")

		days := 7

		if v := c.Query("days"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 90 {
				days = n
			}
		}

		now := time.Now().UTC()
		// Day buckets are aligned to UTC midnight; the window covers the last
		// `days` days up to and including the current (partial) day.
		startDay := now.Truncate(24*time.Hour).AddDate(0, 0, -(days - 1))

		query := fmt.Sprintf(
			"workspace:=%s | stats by (_time:1d) count() total",
			logsQLQuoteValue(workspace),
		)

		params := url.Values{}
		params.Set("start", startDay.Format(time.RFC3339))
		params.Set("end", now.Format(time.RFC3339))

		counts, err := queryAITraceDayCounts(deps, query, params)
		if err != nil {
			klog.Errorf("ai-trace: stats: %v", err)
			c.JSON(http.StatusBadGateway, gin.H{
				"error": "failed to query trace store",
			})

			return
		}

		out := make([]AITraceDayCount, 0, days)

		for i := 0; i < days; i++ {
			date := startDay.AddDate(0, 0, i).Format("2006-01-02")
			out = append(out, AITraceDayCount{Date: date, Count: counts[date]})
		}

		c.JSON(http.StatusOK, AITraceStatsResponse{Days: out})
	}
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
