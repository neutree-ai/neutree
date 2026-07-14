package client

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// TracesService handles communication with the AI trace (access log) endpoints.
//
// Access logs are modeled server-side as AI traces, stored in VictoriaLogs and
// exposed under /api/v1/ai-traces/:workspace. These are custom routes (not
// PostgREST tables), so they cannot go through GenericService.
type TracesService struct {
	client *Client
}

// NewTracesService creates a new traces service.
func NewTracesService(client *Client) *TracesService {
	return &TracesService{client: client}
}

// AllWorkspaces is the sentinel workspace value that aggregates traces across
// every workspace the caller may read. Mirrors the server's allWorkspacesSentinel.
const AllWorkspaces = "_all_"

// MaxTracePageSize is the largest page the list endpoint accepts per request.
const MaxTracePageSize = 500

// AITrace is one inference trace (access log) record. Mirrors the server-side
// shape in internal/routes/logs/ai_trace.go. The list endpoint leaves
// RequestBody/ResponseBody empty; the detail endpoint populates them.
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

// TraceListFilters are the optional server-side filters for a list query. Empty
// fields are omitted from the request.
type TraceListFilters struct {
	EndpointName string
	EndpointType string
	Status       string
	APIKeyID     string
	FinishReason string
	Model        string
	Start        string // RFC3339 lower time bound (inclusive)
	End          string // RFC3339 upper time bound
}

// aiTraceListResponse is the wire format for GET /api/v1/ai-traces/:workspace.
type aiTraceListResponse struct {
	Items      []AITrace `json:"items"`
	NextBefore string    `json:"next_before"`
}

// ListPage fetches a single page of traces for the workspace, applying the
// filters. `before` is the pagination cursor from the previous page's
// NextBefore (empty for the first page); `limit` is capped at MaxTracePageSize.
// It returns the page's items and the cursor for the next (older) page, which
// is empty when the server signals there are no more records.
//
// Callers exporting the full history must deduplicate by RequestID across
// pages: the server's cursor is timestamp-based and inclusive, so the boundary
// record can repeat on the next page.
func (s *TracesService) ListPage(workspace string, filters TraceListFilters, before string, limit int) (items []AITrace, nextBefore string, err error) {
	if workspace == "" {
		return nil, "", fmt.Errorf("workspace is required")
	}

	if limit <= 0 || limit > MaxTracePageSize {
		limit = MaxTracePageSize
	}

	params := url.Values{}
	params.Set("limit", strconv.Itoa(limit))

	setIfNotEmpty(params, "endpoint_name", filters.EndpointName)
	setIfNotEmpty(params, "endpoint_type", filters.EndpointType)
	setIfNotEmpty(params, "status", filters.Status)
	setIfNotEmpty(params, "api_key_id", filters.APIKeyID)
	setIfNotEmpty(params, "finish_reason", filters.FinishReason)
	setIfNotEmpty(params, "model", filters.Model)
	setIfNotEmpty(params, "start", filters.Start)

	// `before` (cursor) takes precedence over an explicit End: it is the upper
	// bound for the next older page.
	if before != "" {
		params.Set("before", before)
	} else {
		setIfNotEmpty(params, "end", filters.End)
	}

	reqURL := fmt.Sprintf("%s/api/v1/ai-traces/%s?%s", s.client.baseURL, url.PathEscape(workspace), params.Encode())

	var resp aiTraceListResponse
	if err := s.getJSON(reqURL, &resp); err != nil {
		return nil, "", err
	}

	return resp.Items, resp.NextBefore, nil
}

// GetDetail fetches a single trace including request/response bodies, looked up
// by request id within the workspace scope.
func (s *TracesService) GetDetail(workspace, requestID string) (*AITrace, error) {
	if workspace == "" {
		return nil, fmt.Errorf("workspace is required")
	}

	if requestID == "" {
		return nil, fmt.Errorf("request id is required")
	}

	reqURL := fmt.Sprintf("%s/api/v1/ai-traces/%s/%s",
		s.client.baseURL, url.PathEscape(workspace), url.PathEscape(requestID))

	var trace AITrace
	if err := s.getJSON(reqURL, &trace); err != nil {
		return nil, err
	}

	return &trace, nil
}

// getJSON performs an authenticated GET and decodes the JSON body into out,
// translating the common trace-store error statuses into readable messages.
func (s *TracesService) getJSON(reqURL string, out any) error {
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return err
	}

	resp, err := s.client.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)

		switch resp.StatusCode {
		case http.StatusServiceUnavailable:
			return fmt.Errorf("access log store is not configured on the server (set --ai-trace-store-url)")
		case http.StatusForbidden:
			return fmt.Errorf("permission denied: the API key needs endpoint:trace-read or external_endpoint:trace-read")
		default:
			return fmt.Errorf("server returned status %d: %s", resp.StatusCode, string(bodyBytes))
		}
	}

	return json.NewDecoder(resp.Body).Decode(out)
}

func setIfNotEmpty(params url.Values, key, value string) {
	if value != "" {
		params.Set(key, value)
	}
}
