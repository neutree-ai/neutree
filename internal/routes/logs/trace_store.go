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

	"github.com/neutree-ai/neutree/internal/util"
)

// traceStore is the only layer that knows how AI inference traces are
// physically stored in VictoriaLogs — the LogsQL dialect, the NDJSON wire
// shapes, and (in the future) the chunked representation of oversized bodies.
// Handlers deal in whole AITrace values and HTTP-level concerns only.
//
// The `scope` string accepted by every method is the LogsQL expression the
// permission layer produced (see traceScopeClause); it is "who may see what",
// not a storage detail, so it crosses the boundary as an opaque clause.
type traceStore struct {
	baseURL string
	client  util.HTTPClient
}

// newTraceStore returns the trace store backed by the configured
// VictoriaLogs URL, or nil when the AI trace store is not configured
// (handlers turn nil into a 503 before any query is attempted).
func newTraceStore(deps *Dependencies) *traceStore {
	if deps.AITraceStoreURL == "" {
		return nil
	}

	return &traceStore{
		baseURL: strings.TrimRight(deps.AITraceStoreURL, "/"),
		client:  deps.HTTPClient,
	}
}

// listProjection is the LogsQL `fields` projection for the list query: every
// metadata column the list view renders, deliberately excluding the large
// request_body / response_body fields so list responses stay small.
const listProjection = "_time, request_id, workspace, endpoint_type, " +
	"endpoint_name, api_key_id, request_uri, request_model, response_model, " +
	"response_status, prompt_tokens, completion_tokens, total_tokens, " +
	"finish_reason, stream, user_agent, duration_ms"

// fullProjection extends listProjection with the large request/response body
// columns. Used only when the caller opts in via ?include_body=true — chiefly
// the CLI export, which fetches bodies inline to avoid an N+1 per-record
// detail lookup.
const fullProjection = listProjection + ", request_body, response_body"

// traceFilters are the caller-facing list filters. The store translates them
// to LogsQL; handlers never build query fragments themselves.
type traceFilters struct {
	EndpointName string
	EndpointType string
	Status       string
	APIKeyID     string
	FinishReason string
	Model        string
}

// clauses renders the filters as LogsQL AND-clauses, in a fixed order.
func (f traceFilters) clauses() []string {
	out := make([]string, 0, 6)

	if f.EndpointName != "" {
		out = append(out, fmt.Sprintf("endpoint_name:=%s", logsQLQuoteValue(f.EndpointName)))
	}

	if f.EndpointType != "" {
		out = append(out, fmt.Sprintf("endpoint_type:=%s", logsQLQuoteValue(f.EndpointType)))
	}

	if f.Status != "" {
		out = append(out, fmt.Sprintf("response_status:=%s", logsQLQuoteValue(f.Status)))
	}

	if f.APIKeyID != "" {
		out = append(out, fmt.Sprintf("api_key_id:=%s", logsQLQuoteValue(f.APIKeyID)))
	}

	if f.FinishReason != "" {
		out = append(out, fmt.Sprintf("finish_reason:=%s", logsQLQuoteValue(f.FinishReason)))
	}

	if f.Model != "" {
		// model can match either request or response model
		out = append(out, fmt.Sprintf(
			"(request_model:=%s OR response_model:=%s)",
			logsQLQuoteValue(f.Model), logsQLQuoteValue(f.Model),
		))
	}

	return out
}

// timeWindow carries the optional RFC3339 start/end bounds of a query straight
// through to the VictoriaLogs HTTP API. Zero values mean "unbounded".
type timeWindow struct {
	Start string
	End   string
}

func (w timeWindow) params() url.Values {
	params := url.Values{}

	if w.Start != "" {
		params.Set("start", w.Start)
	}

	if w.End != "" {
		params.Set("end", w.End)
	}

	return params
}

// baseQuery prepends the permission scope to every query the store issues.
// Storage-level record filtering (e.g. excluding non-trace record types)
// belongs here so no query path can forget it.
func (s *traceStore) baseQuery(scope string) string {
	return scope
}

// List returns up to limit traces, newest first. Bodies are included only
// when includeBody is set; otherwise the projection omits them so responses
// stay small.
func (s *traceStore) List(scope string, f traceFilters, limit int, includeBody bool, w timeWindow) ([]AITrace, error) {
	queryParts := append([]string{s.baseQuery(scope)}, f.clauses()...)

	projection := listProjection
	if includeBody {
		projection = fullProjection
	}

	query := strings.Join(queryParts, " ") +
		" | sort by (_time) desc | limit " + strconv.Itoa(limit) +
		" | fields " + projection

	return s.queryTraces(query, w.params())
}

// Get returns the single trace with the given request id — including the full
// request/response bodies — or nil when no matching record exists within the
// caller's scope.
func (s *traceStore) Get(scope, requestID string) (*AITrace, error) {
	query := fmt.Sprintf(
		"%s request_id:=%s | sort by (_time) desc | limit 1",
		s.baseQuery(scope), logsQLQuoteValue(requestID),
	)

	items, err := s.queryTraces(query, url.Values{})
	if err != nil {
		return nil, err
	}

	if len(items) == 0 {
		return nil, nil
	}

	return &items[0], nil
}

// DayCounts returns per-UTC-day trace counts for [start, end), keyed by
// YYYY-MM-DD.
func (s *traceStore) DayCounts(scope string, start, end time.Time) (map[string]int, error) {
	query := fmt.Sprintf("%s | stats by (_time:1d) count() total", s.baseQuery(scope))

	params := url.Values{}
	params.Set("start", start.Format(time.RFC3339))
	params.Set("end", end.Format(time.RFC3339))

	return s.queryDayCounts(query, params)
}

// KeyStats returns per-API-key aggregates (request count, tokens, success
// count, average latency) since the given instant.
func (s *traceStore) KeyStats(scope string, since time.Time) ([]AITraceKeyStat, error) {
	// Success = 2xx/3xx response_status (regex match, robust to the field being
	// stored as a string); tokens sums total_tokens (missing => 0); avg latency
	// over duration_ms. Empty api_key_id rows (untagged traffic) are dropped by
	// the parser.
	query := fmt.Sprintf(
		"%s | stats by (api_key_id) count() requests, "+
			"sum(total_tokens) tokens, avg(duration_ms) avg_duration_ms, "+
			"count() if (response_status:~\"^[23]\") success",
		s.baseQuery(scope),
	)

	params := url.Values{}
	params.Set("start", since.Format(time.RFC3339))

	return s.queryKeyStats(query, params)
}

// select runs a LogsQL query against VictoriaLogs and returns the raw NDJSON
// response body; the caller must Close it.
func (s *traceStore) selectQuery(query string, params url.Values) (*http.Response, error) {
	params.Set("query", query)
	reqURL := s.baseURL + "/select/logsql/query?" + params.Encode()

	resp, err := s.client.Get(reqURL)
	if err != nil {
		return nil, fmt.Errorf("query victorialogs: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()

		return nil, fmt.Errorf("victorialogs returned status %d", resp.StatusCode)
	}

	return resp, nil
}

// queryTraces runs a LogsQL query and decodes the NDJSON response into
// AITrace records.
func (s *traceStore) queryTraces(query string, params url.Values) ([]AITrace, error) {
	resp, err := s.selectQuery(query, params)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

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

// queryDayCounts runs a `stats by (_time:1d)` LogsQL query and returns a map
// of UTC date (YYYY-MM-DD) to request count.
func (s *traceStore) queryDayCounts(query string, params url.Values) (map[string]int, error) {
	resp, err := s.selectQuery(query, params)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

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

// queryKeyStats runs the per-key `stats by (api_key_id)` aggregation and
// decodes the NDJSON result rows (all values arrive as strings from VL).
func (s *traceStore) queryKeyStats(query string, params url.Values) ([]AITraceKeyStat, error) {
	resp, err := s.selectQuery(query, params)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	out := make([]AITraceKeyStat, 0, 16)
	scanner := bufio.NewScanner(resp.Body)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var r struct {
			APIKeyID      string `json:"api_key_id"`
			Requests      string `json:"requests"`
			Tokens        string `json:"tokens"`
			AvgDurationMs string `json:"avg_duration_ms"`
			Success       string `json:"success"`
		}

		if err := json.Unmarshal(line, &r); err != nil {
			continue
		}

		// Drop the untagged bucket (requests with no api_key_id): it cannot be
		// attributed to any key in the ranking.
		if strings.TrimSpace(r.APIKeyID) == "" {
			continue
		}

		// VL may format count()/sum() results as plain ints or floats
		// ("1240000" or "1.24e6"); parse as float and truncate for robustness.
		stat := AITraceKeyStat{
			APIKeyID:      r.APIKeyID,
			Requests:      parseIntLoose(r.Requests),
			Tokens:        parseIntLoose(r.Tokens),
			Success:       parseIntLoose(r.Success),
			AvgDurationMs: parseFloatLoose(r.AvgDurationMs),
		}

		out = append(out, stat)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan victorialogs response: %w", err)
	}

	return out, nil
}

// parseIntLoose parses a VL numeric result (which may be int- or float-
// formatted) into an int64, truncating any fractional part. Returns 0 on error.
// Integer-formatted values parse as base-10 int64 first so large counts
// (>= 2^53) keep full precision; only float/scientific forms fall back to float.
func parseIntLoose(s string) int64 {
	s = strings.TrimSpace(s)
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}

	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return int64(f)
	}

	return 0
}

// parseFloatLoose parses a VL numeric result into a float64, returning 0 on error.
func parseFloatLoose(s string) float64 {
	if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
		return f
	}

	return 0
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
		Stream:        r.IsStream == stringTrue,
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
