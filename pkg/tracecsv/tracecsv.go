// Package tracecsv defines the CSV layout of an AI trace (access log) export.
// The CLI export and the server's export endpoint both render rows with it, so
// the two outputs keep the same columns.
package tracecsv

import (
	"strconv"

	"github.com/neutree-ai/neutree/pkg/client"
)

// Header lists the trace columns in the order Row emits them.
var Header = []string{
	"request_id", "time", "workspace", "endpoint_type", "endpoint_name",
	"api_key_id", "request_uri", "request_model", "response_model",
	"response_status", "prompt_tokens", "completion_tokens", "total_tokens",
	"finish_reason", "stream", "user_agent", "duration_ms",
	"request_body", "response_body", "body_truncated", "body_incomplete", "upstream", "upstream_model",
}

// Row renders a trace as a CSV row. Body columns are empty when the trace was
// fetched without bodies.
func Row(t client.AITrace) []string {
	return []string{
		t.RequestID, t.Time, t.Workspace, t.EndpointType, t.EndpointName,
		t.APIKeyID, t.RequestURI, t.RequestModel, t.ResponseModel,
		strconv.Itoa(t.ResponseStatus),
		intPtrStr(t.PromptTokens), intPtrStr(t.CompletionTokens), intPtrStr(t.TotalTokens),
		t.FinishReason, strconv.FormatBool(t.Stream), t.UserAgent, intPtrStr(t.DurationMs),
		t.RequestBody, t.ResponseBody,
		strconv.FormatBool(t.BodyTruncated), strconv.FormatBool(t.BodyIncomplete), t.Upstream, t.UpstreamModel,
	}
}

// intPtrStr renders a *int as its decimal string, or "" when nil.
func intPtrStr(p *int) string {
	if p == nil {
		return ""
	}

	return strconv.Itoa(*p)
}
