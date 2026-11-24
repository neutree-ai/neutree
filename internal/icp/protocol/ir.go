package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/neutree-ai/neutree/internal/icp/quota"
)

const (
	ProtocolOpenAIChat        = "openai_chat"
	ProtocolAnthropicMessages = "anthropic_messages"
	ProtocolOpenAIResponses   = "openai_responses"

	FinishStop      = "stop"
	FinishToolCalls = "tool_calls"
	FinishLength    = "length"

	ToolChoiceAuto     = "auto"
	ToolChoiceNone     = "none"
	ToolChoiceRequired = "required"
	ToolChoiceFunction = "function"

	jsonNullLiteral      = "null"
	itemFunctionCall     = "function_call"
	itemReasoning        = "reasoning"
	respStatusCompleted  = "completed"
	respStatusIncomplete = "incomplete"
)

const (
	BlockText       = "text"
	BlockImage      = "image"
	BlockThinking   = "thinking"
	BlockToolUse    = "tool_use"
	BlockToolResult = "tool_result"
)

const (
	EventMessageStart  = "message_start"
	EventTextDelta     = "text_delta"
	EventThinkingDelta = "thinking_delta"
	EventToolCallStart = "tool_call_start"
	EventToolCallDelta = "tool_call_delta"
	EventToolCallDone  = "tool_call_done"
	EventUsage         = "usage"
	EventMessageStop   = "message_stop"
	EventError         = "error"
)

type ContentBlock struct {
	Type             string `json:"type"`
	Text             string `json:"text,omitempty"`
	ImageURL         string `json:"image_url,omitempty"`
	MediaType        string `json:"media_type,omitempty"`
	ImageData        string `json:"image_data,omitempty"`
	Signature        string `json:"signature,omitempty"`
	EncryptedContent string `json:"encrypted_content,omitempty"`
	SignatureOrigin  string `json:"signature_origin,omitempty"`
	ToolCallID       string `json:"tool_call_id,omitempty"`
	ToolName         string `json:"tool_name,omitempty"`
	ToolArguments    string `json:"tool_arguments,omitempty"`
	IsError          bool   `json:"is_error,omitempty"`
}

type ToolChoice struct {
	Mode string `json:"mode"`
	Name string `json:"name,omitempty"`
}

func ParseToolChoice(raw json.RawMessage) *ToolChoice {
	if len(raw) == 0 || string(raw) == jsonNullLiteral {
		return nil
	}

	var mode string
	if json.Unmarshal(raw, &mode) == nil && mode != "" {
		if mode == "any" {
			mode = ToolChoiceRequired
		}

		return &ToolChoice{Mode: mode}
	}

	var obj struct {
		Type     string `json:"type"`
		Name     string `json:"name"`
		Function *struct {
			Name string `json:"name"`
		} `json:"function"`
	}

	if json.Unmarshal(raw, &obj) != nil || obj.Type == "" {
		return nil
	}

	switch obj.Type {
	case ToolChoiceAuto, ToolChoiceNone, ToolChoiceRequired:
		return &ToolChoice{Mode: obj.Type}
	case "any":
		return &ToolChoice{Mode: ToolChoiceRequired}
	case "tool", ToolChoiceFunction:
		name := obj.Name
		if name == "" && obj.Function != nil {
			name = obj.Function.Name
		}

		if name != "" {
			return &ToolChoice{Mode: ToolChoiceFunction, Name: name}
		}
	}

	return nil
}

type Message struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type ToolCall struct {
	Index     int    `json:"index"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type ErrorDetail struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type CanonicalRequest struct {
	SourceProtocol       string         `json:"source_protocol,omitempty"`
	Model                string         `json:"model"`
	System               []ContentBlock `json:"system,omitempty"`
	Messages             []Message      `json:"messages,omitempty"`
	Tools                []Tool         `json:"tools,omitempty"`
	ToolChoice           *ToolChoice    `json:"tool_choice,omitempty"`
	MaxTokens            *int           `json:"max_tokens,omitempty"`
	Temperature          *float64       `json:"temperature,omitempty"`
	TopP                 *float64       `json:"top_p,omitempty"`
	Stop                 []string       `json:"stop,omitempty"`
	Stream               bool           `json:"stream,omitempty"`
	ClientRequestedUsage bool           `json:"client_requested_usage,omitempty"`
	ReasoningEffort      string         `json:"reasoning_effort,omitempty"`
	ThinkingBudget       *int           `json:"thinking_budget,omitempty"`
	PreviousResponseID   string         `json:"previous_response_id,omitempty"`
	Store                bool           `json:"store,omitempty"`
	StatefulTools        []string       `json:"stateful_tools,omitempty"`
}

type StreamEvent struct {
	Type         string          `json:"type"`
	Index        int             `json:"index,omitempty"`
	ResponseID   string          `json:"response_id,omitempty"`
	Model        string          `json:"model,omitempty"`
	Role         string          `json:"role,omitempty"`
	Text         string          `json:"text,omitempty"`
	Signature    string          `json:"signature,omitempty"`
	Encrypted    string          `json:"encrypted,omitempty"`
	Origin       string          `json:"origin,omitempty"`
	ToolCall     *ToolCall       `json:"tool_call,omitempty"`
	Usage        []quota.Measure `json:"usage,omitempty"`
	FinishReason string          `json:"finish_reason,omitempty"`
	Error        *ErrorDetail    `json:"error,omitempty"`
}

type CanonicalResponse struct {
	ID           string          `json:"id"`
	Model        string          `json:"model"`
	Content      []ContentBlock  `json:"content"`
	FinishReason string          `json:"finish_reason"`
	Usage        []quota.Measure `json:"usage,omitempty"`
}

type UnsupportedFeatureError struct {
	Feature string
	Target  string
}

func (e *UnsupportedFeatureError) Error() string {
	return fmt.Sprintf("feature %q is not supported by stateless upstream protocol %q", e.Feature, e.Target)
}

func (e *UnsupportedFeatureError) StatusCode() int {
	return http.StatusBadRequest
}

func (e *UnsupportedFeatureError) JSON() []byte {
	b, _ := json.Marshal(map[string]any{
		"error": map[string]string{
			"type":    "unsupported_feature",
			"message": e.Error(),
		},
	})

	return b
}

func ValidateTarget(req CanonicalRequest, targetProtocol string) error {
	if targetProtocol == ProtocolOpenAIResponses {
		return nil
	}

	if req.PreviousResponseID != "" {
		return &UnsupportedFeatureError{Feature: "previous_response_id", Target: targetProtocol}
	}

	if req.Store {
		return &UnsupportedFeatureError{Feature: "store", Target: targetProtocol}
	}

	if len(req.StatefulTools) > 0 {
		return &UnsupportedFeatureError{Feature: "built-in tool " + req.StatefulTools[0], Target: targetProtocol}
	}

	return nil
}

// SanitizeForTarget strips cross-vendor encrypted/signed thinking metadata when targeting a different protocol.
func SanitizeForTarget(req CanonicalRequest, targetModel, targetProtocol string) (CanonicalRequest, error) {
	if err := ValidateTarget(req, targetProtocol); err != nil {
		return CanonicalRequest{}, err
	}

	out := req
	if targetModel != "" {
		out.Model = targetModel
	}

	msgs := make([]Message, len(req.Messages))

	for i, m := range req.Messages {
		blocks := make([]ContentBlock, 0, len(m.Content))

		for _, b := range m.Content {
			if b.Type == BlockThinking && b.SignatureOrigin != "" && b.SignatureOrigin != targetProtocol {
				b.Signature = ""
				b.EncryptedContent = ""
			}

			blocks = append(blocks, b)
		}

		msgs[i] = Message{Role: m.Role, Content: blocks}
	}

	out.Messages = msgs

	return out, nil
}

// PrefixHash computes a tenant-, route-, and model-isolated digest over system instructions and leading messages.
func PrefixHash(workspace, routeKey, model string, req CanonicalRequest) string {
	h := sha256.New()
	_, _ = h.Write([]byte(workspace + "\x00" + routeKey + "\x00" + model + "\x00"))

	for _, s := range req.System {
		_, _ = h.Write([]byte(s.Text + "\x00"))
	}

	limit := len(req.Messages)
	if limit > 1 {
		limit--
	}

	for i := 0; i < limit; i++ {
		_, _ = h.Write([]byte(req.Messages[i].Role + ":"))
		for _, b := range req.Messages[i].Content {
			_, _ = h.Write([]byte(b.Type + "=" + b.Text + b.ToolName + b.ToolArguments + "\x00"))
		}
	}

	return hex.EncodeToString(h.Sum(nil)[:16])
}

// FoldEvents collapses a stream of IR events into a single CanonicalResponse.
func FoldEvents(defaultModel string, events []StreamEvent) CanonicalResponse {
	resp := CanonicalResponse{
		ID:           "resp-icp",
		Model:        defaultModel,
		FinishReason: FinishStop,
	}

	var textBuf, thinkBuf, sigBuf, encBuf, origin string
	toolOrder := []int{}
	toolsByIdx := map[int]*ToolCall{}

	flushText := func() {
		if thinkBuf != "" || sigBuf != "" || encBuf != "" {
			resp.Content = append(resp.Content, ContentBlock{
				Type:             BlockThinking,
				Text:             thinkBuf,
				Signature:        sigBuf,
				EncryptedContent: encBuf,
				SignatureOrigin:  origin,
			})
			thinkBuf, sigBuf, encBuf = "", "", ""
		}

		if textBuf != "" {
			resp.Content = append(resp.Content, ContentBlock{Type: BlockText, Text: textBuf})
			textBuf = ""
		}
	}

	for _, ev := range events {
		switch ev.Type {
		case EventMessageStart:
			if ev.ResponseID != "" {
				resp.ID = ev.ResponseID
			}

			if ev.Model != "" {
				resp.Model = ev.Model
			}
		case EventThinkingDelta:
			thinkBuf += ev.Text

			if ev.Signature != "" {
				sigBuf = ev.Signature
			}

			if ev.Encrypted != "" {
				encBuf = ev.Encrypted
			}

			if ev.Origin != "" {
				origin = ev.Origin
			}
		case EventTextDelta:
			textBuf += ev.Text
		case EventToolCallStart, EventToolCallDelta, EventToolCallDone:
			if ev.ToolCall == nil {
				continue
			}

			flushText()

			tc, ok := toolsByIdx[ev.ToolCall.Index]
			if !ok {
				tc = &ToolCall{Index: ev.ToolCall.Index}
				toolsByIdx[ev.ToolCall.Index] = tc

				toolOrder = append(toolOrder, ev.ToolCall.Index)
			}

			if ev.ToolCall.ID != "" {
				tc.ID = ev.ToolCall.ID
			}

			if ev.ToolCall.Name != "" {
				tc.Name = ev.ToolCall.Name
			}

			if ev.Type == EventToolCallDone && ev.ToolCall.Arguments != "" && tc.Arguments == "" {
				tc.Arguments = ev.ToolCall.Arguments
			} else if ev.Type != EventToolCallDone {
				tc.Arguments += ev.ToolCall.Arguments
			}
		case EventUsage:
			resp.Usage = ev.Usage
		case EventMessageStop:
			if ev.FinishReason != "" {
				resp.FinishReason = ev.FinishReason
			}
		}
	}

	flushText()

	for _, idx := range toolOrder {
		tc := toolsByIdx[idx]
		resp.Content = append(resp.Content, ContentBlock{
			Type:          BlockToolUse,
			ToolCallID:    tc.ID,
			ToolName:      tc.Name,
			ToolArguments: tc.Arguments,
		})
	}

	if len(toolOrder) > 0 && resp.FinishReason == FinishStop {
		resp.FinishReason = FinishToolCalls
	}

	return resp
}

// ExpandResponse converts a CanonicalResponse into canonical StreamEvents.
func ExpandResponse(resp CanonicalResponse) []StreamEvent {
	events := []StreamEvent{{
		Type:       EventMessageStart,
		ResponseID: resp.ID,
		Model:      resp.Model,
		Role:       "assistant",
	}}

	toolIdx := 0

	for i, b := range resp.Content {
		switch b.Type {
		case BlockThinking:
			events = append(events, StreamEvent{
				Type:      EventThinkingDelta,
				Index:     i,
				Text:      b.Text,
				Signature: b.Signature,
				Encrypted: b.EncryptedContent,
				Origin:    b.SignatureOrigin,
			})
		case BlockText:
			events = append(events, StreamEvent{
				Type:  EventTextDelta,
				Index: i,
				Text:  b.Text,
			})
		case BlockToolUse:
			tc := &ToolCall{Index: toolIdx, ID: b.ToolCallID, Name: b.ToolName}
			events = append(events,
				StreamEvent{Type: EventToolCallStart, Index: toolIdx, ToolCall: tc},
				StreamEvent{Type: EventToolCallDelta, Index: toolIdx, ToolCall: &ToolCall{Index: toolIdx, ID: b.ToolCallID, Arguments: b.ToolArguments}},
				StreamEvent{Type: EventToolCallDone, Index: toolIdx, ToolCall: &ToolCall{Index: toolIdx, ID: b.ToolCallID, Name: b.ToolName, Arguments: b.ToolArguments}},
			)
			toolIdx++
		}
	}

	if len(resp.Usage) > 0 {
		events = append(events, StreamEvent{Type: EventUsage, Usage: resp.Usage})
	}

	reason := resp.FinishReason
	if reason == "" {
		reason = FinishStop
	}

	events = append(events, StreamEvent{Type: EventMessageStop, FinishReason: reason})

	return events
}
