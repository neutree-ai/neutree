package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/neutree-ai/neutree/internal/icp/protocol"
	"github.com/neutree-ai/neutree/internal/icp/quota"
	"github.com/neutree-ai/neutree/internal/icp/state"
)

type Track string

const (
	TrackPassthrough Track = "passthrough"
	TrackTranslated  Track = "translated"
)

type ErrorBody struct {
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}

type Response struct {
	Status  int
	Headers http.Header
	Body    []byte
	Error   *ErrorBody
}

func errorResponse(status int, errType, code, message string) *Response {
	return &Response{
		Status: status,
		Error: &ErrorBody{
			Type:    errType,
			Code:    code,
			Message: message,
		},
	}
}

type RequestContext struct {
	Context context.Context
	Cancel  context.CancelCauseFunc
	Writer  http.ResponseWriter
	Request *http.Request

	Snapshot       *state.ConfigSnapshot
	Route          state.Route
	APIKey         state.APIKey
	RequestID      string
	Track          Track
	SourceProtocol string
	Model          string
	IsModelList    bool

	RequestBody       io.Reader
	RequestBodyLength int64
	CanonicalRequest  *protocol.CanonicalRequest
	ResponseStatus    int
	Usage             *quota.UsageRecord
}

type AttemptDisposition string

const (
	AttemptRetry   AttemptDisposition = "retry"
	AttemptDeliver AttemptDisposition = "deliver"
	AttemptAbort   AttemptDisposition = "abort"
)

type AttemptSummary struct {
	ID          string             `json:"id"`
	UpstreamID  string             `json:"upstream_id,omitempty"`
	TargetModel string             `json:"target_model,omitempty"`
	Status      int                `json:"status"`
	Err         string             `json:"error,omitempty"`
	Disposition AttemptDisposition `json:"disposition"`
	Duration    time.Duration      `json:"duration"`
	TTFT        time.Duration      `json:"ttft,omitempty"`
	Synthetic   bool               `json:"synthetic,omitempty"`
}

type AttemptContext struct {
	Request         *RequestContext
	Context         context.Context
	Cancel          context.CancelFunc
	ID              string
	Upstream        state.Upstream
	TargetModel     string
	UpstreamRequest *http.Request
	Response        *http.Response
	StatusCode      int
	Err             error
	Disposition     AttemptDisposition
	TrustedUsage    bool
	Stream          bool
	Synthetic       bool
	StartedAt       time.Time
	FirstByteAt     time.Time
}

type Plugin interface {
	Name() string
	Begin(rc *RequestContext) RequestHook
}

// RequestHook covers the outer request plane (implemented by every active plugin session).
type RequestHook interface {
	OnIngress(rc *RequestContext) *Response
	OnRequest(rc *RequestContext) *Response
	OnComplete(rc *RequestContext)
}

// AttemptHook covers the inner physical/synthetic upstream attempt plane.
type AttemptHook interface {
	OnAttemptStart(ac *AttemptContext) *Response
	OnAttemptResponse(ac *AttemptContext) *Response
	OnAttemptDone(ac *AttemptContext)
}

// SemanticHook covers the translated AI IR plane (non-streaming CanonicalResponse & streaming StreamEvents).
type SemanticHook interface {
	OnCanonicalResponse(ac *AttemptContext, resp *protocol.CanonicalResponse) *Response
	OnStreamEvent(ac *AttemptContext, ev *protocol.StreamEvent) error
}

type hookEntry struct {
	request  RequestHook
	attempt  AttemptHook
	semantic SemanticHook
}

type hookStack struct {
	entries []hookEntry
}

func (s *Server) beginHooks(rc *RequestContext) (*hookStack, *Response) {
	stack := &hookStack{
		entries: make([]hookEntry, 0, len(s.plugins)),
	}

	for _, plugin := range s.plugins {
		if plugin == nil {
			continue
		}

		h := plugin.Begin(rc)
		if h == nil {
			continue
		}

		entry := hookEntry{request: h}

		if ah, ok := h.(AttemptHook); ok {
			entry.attempt = ah
		}

		if sh, ok := h.(SemanticHook); ok {
			entry.semantic = sh
		}

		stack.entries = append(stack.entries, entry)

		if resp := h.OnIngress(rc); resp != nil {
			return stack, resp
		}
	}

	return stack, nil
}

func (s *hookStack) onRequest(rc *RequestContext) *Response {
	for _, entry := range s.entries {
		if resp := entry.request.OnRequest(rc); resp != nil {
			return resp
		}
	}

	return nil
}

func (s *hookStack) complete(rc *RequestContext) {
	for i := len(s.entries) - 1; i >= 0; i-- {
		s.entries[i].request.OnComplete(rc)
	}
}

func (s *hookStack) attemptStart(ac *AttemptContext) *Response {
	for _, entry := range s.entries {
		if entry.attempt == nil {
			continue
		}

		if resp := entry.attempt.OnAttemptStart(ac); resp != nil {
			return resp
		}
	}

	return nil
}

func (s *hookStack) attemptResponse(ac *AttemptContext) *Response {
	for _, entry := range s.entries {
		if entry.attempt == nil {
			continue
		}

		if resp := entry.attempt.OnAttemptResponse(ac); resp != nil {
			return resp
		}
	}

	return nil
}

func (s *hookStack) attemptDone(ac *AttemptContext) {
	for i := len(s.entries) - 1; i >= 0; i-- {
		if s.entries[i].attempt != nil {
			s.entries[i].attempt.OnAttemptDone(ac)
		}
	}
}

func (s *hookStack) canonicalResponse(ac *AttemptContext, resp *protocol.CanonicalResponse) *Response {
	for _, entry := range s.entries {
		if entry.semantic == nil {
			continue
		}

		if out := entry.semantic.OnCanonicalResponse(ac, resp); out != nil {
			return out
		}
	}

	return nil
}

func (s *hookStack) streamEvent(ac *AttemptContext, ev *protocol.StreamEvent) error {
	for _, entry := range s.entries {
		if entry.semantic == nil {
			continue
		}

		if err := entry.semantic.OnStreamEvent(ac, ev); err != nil {
			return err
		}
	}

	return nil
}

func writePluginResponse(w http.ResponseWriter, rc *RequestContext, resp *Response) {
	if resp == nil {
		return
	}

	for key, values := range resp.Headers {
		w.Header()[key] = append([]string(nil), values...)
	}

	status := resp.Status
	if status == 0 {
		status = http.StatusInternalServerError
	}

	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}

	rc.ResponseStatus = status
	w.WriteHeader(status)

	if resp.Body != nil {
		_, _ = w.Write(resp.Body)
		return
	}

	if resp.Error == nil {
		return
	}

	if rc.SourceProtocol == protocol.ProtocolAnthropicMessages {
		b, _ := json.Marshal(map[string]any{
			"type":  "error",
			"error": resp.Error,
		})
		_, _ = w.Write(b)

		return
	}

	b, _ := json.Marshal(map[string]any{
		"error": resp.Error,
	})
	_, _ = w.Write(b)
}
