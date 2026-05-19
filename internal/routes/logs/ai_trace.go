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

// AITrace is one row in the inference trace list returned to the SPA.
//
// Mirrors the shape Vector pushes to VictoriaLogs, with response_status
// normalised to int for the UI's filtering convenience.
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

// AITraceListResponse is the wire format for GET /api/v1/ai-traces/:workspace.
//
// NextBefore is the cursor for the next page: passing it as ?before=<ts> on
// the next call returns the next batch. Empty when there are no older records.
type AITraceListResponse struct {
	Items      []AITrace `json:"items"`
	NextBefore string    `json:"next_before,omitempty"`
}

// RegisterAITraceRoutes mounts the AI inference trace listing endpoint.
func RegisterAITraceRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	traces := group.Group("/ai-traces/:workspace")
	traces.Use(middlewares...)
	traces.Use(middleware.RequireWorkspacePermission("workspace:read", middleware.PermissionDependencies{
		Storage: deps.Storage,
	}))

	traces.GET("", handleListAITraces(deps))
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
		if model := strings.TrimSpace(c.Query("model")); model != "" {
			// model can match either request or response model
			queryParts = append(queryParts, fmt.Sprintf(
				"(request_model:=%s OR response_model:=%s)",
				logsQLQuoteValue(model), logsQLQuoteValue(model),
			))
		}

		query := strings.Join(queryParts, " ") + " | sort by (_time) desc | limit " + strconv.Itoa(limit)

		params := url.Values{}
		params.Set("query", query)
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

		reqURL := strings.TrimRight(deps.AITraceStoreURL, "/") + "/select/logsql/query?" + params.Encode()

		resp, err := deps.HTTPClient.Get(reqURL)
		if err != nil {
			klog.Errorf("ai-trace: failed to query victorialogs: %v", err)
			c.JSON(http.StatusBadGateway, gin.H{
				"error": "failed to query trace store",
			})
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			klog.Errorf("ai-trace: victorialogs returned %d", resp.StatusCode)
			c.JSON(http.StatusBadGateway, gin.H{
				"error":  "trace store returned non-200",
				"status": resp.StatusCode,
			})
			return
		}

		items := make([]AITrace, 0, limit)
		scanner := bufio.NewScanner(resp.Body)
		// VL records can be large because they embed full request/response bodies.
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
			klog.Errorf("ai-trace: scan victorialogs response: %v", err)
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

// vlRecord matches the shape Vector writes to VictoriaLogs.
// All values come back as strings; we coerce types we care about.
type vlRecord struct {
	Time             string `json:"_time"`
	Stream           string `json:"_stream,omitempty"`
	Msg              string `json:"_msg,omitempty"`
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

	responseBody := r.ResponseBody
	if responseBody == "" {
		// VL stores the configured _msg_field under "_msg"; recover it.
		responseBody = r.Msg
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
		ResponseBody:  responseBody,
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
