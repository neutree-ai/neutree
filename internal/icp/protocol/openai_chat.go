package protocol

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/neutree-ai/neutree/internal/icp/quota"
)

type ChatCodec struct{}

func (ChatCodec) Name() string {
	return ProtocolOpenAIChat
}

func (ChatCodec) EndpointURL(baseURL string) string {
	return joinProtocolPath(baseURL, "/v1/chat/completions", "/chat/completions")
}

type chatWireReq struct {
	Model               string            `json:"model"`
	Messages            []chatWireMessage `json:"messages"`
	Tools               []chatWireTool    `json:"tools,omitempty"`
	ToolChoice          json.RawMessage   `json:"tool_choice,omitempty"`
	MaxTokens           *int              `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int              `json:"max_completion_tokens,omitempty"`
	Temperature         *float64          `json:"temperature,omitempty"`
	TopP                *float64          `json:"top_p,omitempty"`
	Stop                json.RawMessage   `json:"stop,omitempty"`
	Stream              bool              `json:"stream,omitempty"`
	StreamOptions       *chatStreamOpts   `json:"stream_options,omitempty"`
	ReasoningEffort     string            `json:"reasoning_effort,omitempty"`
}

type chatStreamOpts struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatWireMessage struct {
	Role             string          `json:"role"`
	Content          json.RawMessage `json:"content,omitempty"`
	ReasoningContent string          `json:"reasoning_content,omitempty"`
	ToolCalls        []chatWireCall  `json:"tool_calls,omitempty"`
	ToolCallID       string          `json:"tool_call_id,omitempty"`
}

type chatWireCall struct {
	Index    *int             `json:"index,omitempty"`
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"`
	Function chatWireFunction `json:"function"`
}

type chatWireFunction struct {
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Arguments   string          `json:"arguments,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type chatWireTool struct {
	Type     string           `json:"type"`
	Function chatWireFunction `json:"function"`
}

func (ChatCodec) DecodeRequest(r io.Reader) (CanonicalRequest, error) {
	var wire chatWireReq
	if err := json.NewDecoder(r).Decode(&wire); err != nil {
		return CanonicalRequest{}, err
	}

	out := CanonicalRequest{
		SourceProtocol:  ProtocolOpenAIChat,
		Model:           wire.Model,
		ToolChoice:      ParseToolChoice(wire.ToolChoice),
		MaxTokens:       wire.MaxTokens,
		Temperature:     wire.Temperature,
		TopP:            wire.TopP,
		Stream:          wire.Stream,
		ReasoningEffort: wire.ReasoningEffort,
	}

	if out.MaxTokens == nil {
		out.MaxTokens = wire.MaxCompletionTokens
	}

	if wire.StreamOptions != nil && wire.StreamOptions.IncludeUsage {
		out.ClientRequestedUsage = true
	}

	out.Stop = parseChatStop(wire.Stop)

	for _, t := range wire.Tools {
		if t.Type != "" && t.Type != "function" {
			out.StatefulTools = append(out.StatefulTools, t.Type)
			continue
		}

		out.Tools = append(out.Tools, Tool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			Parameters:  t.Function.Parameters,
		})
	}

	for _, m := range wire.Messages {
		blocks := parseChatContent(m.Content)
		if m.Role == "system" || m.Role == "developer" {
			out.System = append(out.System, blocks...)
			continue
		}

		if m.Role == "tool" {
			txt := joinBlockText(blocks)
			out.Messages = append(out.Messages, Message{
				Role: "user",
				Content: []ContentBlock{{
					Type:       BlockToolResult,
					ToolCallID: m.ToolCallID,
					Text:       txt,
				}},
			})

			continue
		}

		var msgBlocks []ContentBlock
		if m.ReasoningContent != "" {
			msgBlocks = append(msgBlocks, ContentBlock{Type: BlockThinking, Text: m.ReasoningContent})
		}

		msgBlocks = append(msgBlocks, blocks...)
		for _, tc := range m.ToolCalls {
			msgBlocks = append(msgBlocks, ContentBlock{
				Type:          BlockToolUse,
				ToolCallID:    tc.ID,
				ToolName:      tc.Function.Name,
				ToolArguments: tc.Function.Arguments,
			})
		}

		out.Messages = append(out.Messages, Message{Role: m.Role, Content: msgBlocks})
	}

	return out, nil
}

func parseChatStop(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}

	var single string
	if json.Unmarshal(raw, &single) == nil && single != "" {
		return []string{single}
	}

	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return list
	}

	return nil
}

func parseChatContent(raw json.RawMessage) []ContentBlock {
	if len(raw) == 0 || string(raw) == jsonNullLiteral {
		return nil
	}

	var str string
	if json.Unmarshal(raw, &str) == nil {
		if str == "" {
			return nil
		}

		return []ContentBlock{{Type: BlockText, Text: str}}
	}

	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL *struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}

	if json.Unmarshal(raw, &parts) != nil {
		return nil
	}

	var blocks []ContentBlock

	for _, p := range parts {
		switch p.Type {
		case BlockText:
			blocks = append(blocks, ContentBlock{Type: BlockText, Text: p.Text})
		case "image_url":
			if p.ImageURL != nil {
				blocks = append(blocks, ContentBlock{Type: BlockImage, ImageURL: p.ImageURL.URL})
			}
		}
	}

	return blocks
}

func joinBlockText(blocks []ContentBlock) string {
	var parts []string

	for _, b := range blocks {
		if b.Text != "" {
			parts = append(parts, b.Text)
		}
	}

	return strings.Join(parts, "\n")
}

func (ChatCodec) EncodeRequest(req CanonicalRequest, targetModel string) ([]byte, error) {
	clean, err := SanitizeForTarget(req, targetModel, ProtocolOpenAIChat)
	if err != nil {
		return nil, err
	}

	out := map[string]any{
		"model": clean.Model,
	}

	var wireMsgs []map[string]any
	if sysText := joinBlockText(clean.System); sysText != "" {
		wireMsgs = append(wireMsgs, map[string]any{"role": "system", "content": sysText})
	}

	for _, m := range clean.Messages {
		wireMsgs = append(wireMsgs, encodeChatMessage(m)...)
	}

	out["messages"] = wireMsgs

	if len(clean.Tools) > 0 {
		tools := make([]chatWireTool, 0, len(clean.Tools))

		for _, t := range clean.Tools {
			params := t.Parameters
			if len(params) == 0 {
				params = json.RawMessage(`{"type":"object","properties":{}}`)
			}

			tools = append(tools, chatWireTool{
				Type: "function",
				Function: chatWireFunction{
					Name:        t.Name,
					Description: t.Description,
					Parameters:  params,
				},
			})
		}

		out["tools"] = tools
	}

	if tc := encodeChatToolChoice(clean.ToolChoice); tc != nil {
		out["tool_choice"] = tc
	}

	if clean.MaxTokens != nil {
		out["max_tokens"] = *clean.MaxTokens
	}

	if clean.Temperature != nil {
		out["temperature"] = *clean.Temperature
	}

	if clean.TopP != nil {
		out["top_p"] = *clean.TopP
	}

	if len(clean.Stop) > 0 {
		out["stop"] = clean.Stop
	}

	if clean.ReasoningEffort != "" {
		out["reasoning_effort"] = clean.ReasoningEffort
	}

	if clean.Stream {
		out["stream"] = true
		out["stream_options"] = map[string]bool{"include_usage": true}
	}

	return json.Marshal(out)
}

func encodeChatToolChoice(tc *ToolChoice) any {
	if tc == nil || tc.Mode == "" {
		return nil
	}

	switch tc.Mode {
	case ToolChoiceAuto, ToolChoiceNone, ToolChoiceRequired:
		return tc.Mode
	case ToolChoiceFunction:
		if tc.Name != "" {
			return map[string]any{
				"type":     ToolChoiceFunction,
				"function": map[string]string{"name": tc.Name},
			}
		}
	}

	return nil
}

func encodeChatMessage(m Message) []map[string]any {
	var out []map[string]any
	var textParts []map[string]any
	var toolCalls []chatWireCall
	var reasoning strings.Builder

	for _, b := range m.Content {
		switch b.Type {
		case BlockToolResult:
			out = append(out, map[string]any{
				"role":         "tool",
				"tool_call_id": b.ToolCallID,
				"content":      b.Text,
			})
		case BlockToolUse:
			args := b.ToolArguments
			if args == "" {
				args = "{}"
			}

			toolCalls = append(toolCalls, chatWireCall{
				ID:       b.ToolCallID,
				Type:     "function",
				Function: chatWireFunction{Name: b.ToolName, Arguments: args},
			})
		case BlockThinking:
			reasoning.WriteString(b.Text)
		case BlockImage:
			url := b.ImageURL
			if url == "" && b.ImageData != "" {
				mt := b.MediaType
				if mt == "" {
					mt = "image/png"
				}

				url = "data:" + mt + ";base64," + b.ImageData
			}

			textParts = append(textParts, map[string]any{"type": "image_url", "image_url": map[string]string{"url": url}})
		case BlockText:
			textParts = append(textParts, map[string]any{"type": BlockText, "text": b.Text})
		}
	}

	if len(textParts) == 0 && len(toolCalls) == 0 && reasoning.Len() == 0 {
		return out
	}

	msg := map[string]any{"role": m.Role}
	if len(textParts) == 1 && textParts[0]["type"] == BlockText {
		msg["content"] = textParts[0]["text"]
	} else if len(textParts) > 0 {
		msg["content"] = textParts
	}

	if reasoning.Len() > 0 {
		msg["reasoning_content"] = reasoning.String()
	}

	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
	}

	return append([]map[string]any{msg}, out...)
}

type chatWireResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message      chatWireMessage `json:"message"`
		FinishReason string          `json:"finish_reason"`
	} `json:"choices"`
	Usage *chatWireUsage `json:"usage,omitempty"`
}

type chatWireUsage struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details,omitempty"`
}

func (ChatCodec) DecodeResponse(r io.Reader) (CanonicalResponse, error) {
	var wire chatWireResponse
	if err := json.NewDecoder(r).Decode(&wire); err != nil {
		return CanonicalResponse{}, err
	}

	resp := CanonicalResponse{
		ID:           wire.ID,
		Model:        wire.Model,
		FinishReason: FinishStop,
	}

	if wire.Usage != nil {
		var cached, reasoning int64
		if wire.Usage.PromptTokensDetails != nil {
			cached = wire.Usage.PromptTokensDetails.CachedTokens
		}

		if wire.Usage.CompletionTokensDetails != nil {
			reasoning = wire.Usage.CompletionTokensDetails.ReasoningTokens
		}

		resp.Usage = usageMeasures(wire.Usage.PromptTokens, wire.Usage.CompletionTokens, 0, cached, reasoning)
	}

	if len(wire.Choices) > 0 {
		ch := wire.Choices[0]
		if ch.FinishReason != "" {
			resp.FinishReason = ch.FinishReason
		}

		if ch.Message.ReasoningContent != "" {
			resp.Content = append(resp.Content, ContentBlock{Type: BlockThinking, Text: ch.Message.ReasoningContent})
		}

		resp.Content = append(resp.Content, parseChatContent(ch.Message.Content)...)
		for _, tc := range ch.Message.ToolCalls {
			resp.Content = append(resp.Content, ContentBlock{
				Type:          BlockToolUse,
				ToolCallID:    tc.ID,
				ToolName:      tc.Function.Name,
				ToolArguments: tc.Function.Arguments,
			})
		}
	}

	return resp, nil
}

func (ChatCodec) EncodeResponse(resp CanonicalResponse, _ CanonicalRequest) ([]byte, error) {
	var text strings.Builder
	var reasoning strings.Builder
	var toolCalls []chatWireCall

	for _, b := range resp.Content {
		switch b.Type {
		case BlockThinking:
			reasoning.WriteString(b.Text)
		case BlockText:
			text.WriteString(b.Text)
		case BlockToolUse:
			toolCalls = append(toolCalls, chatWireCall{
				ID:       b.ToolCallID,
				Type:     "function",
				Function: chatWireFunction{Name: b.ToolName, Arguments: b.ToolArguments},
			})
		}
	}

	msg := map[string]any{"role": "assistant"}
	if text.Len() > 0 || len(toolCalls) == 0 {
		msg["content"] = text.String()
	} else {
		msg["content"] = nil
	}

	if reasoning.Len() > 0 {
		msg["reasoning_content"] = reasoning.String()
	}

	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
	}

	inTok := measureVal(resp.Usage, "input_tokens")
	outTok := measureVal(resp.Usage, "output_tokens")

	id := resp.ID
	if id == "" {
		id = "chatcmpl-icp"
	}

	reason := resp.FinishReason
	if reason == "" {
		reason = FinishStop
	}

	return json.Marshal(map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"model":   resp.Model,
		"choices": []map[string]any{{"index": 0, "message": msg, "finish_reason": reason}},
		"usage": map[string]int64{
			"prompt_tokens":     inTok,
			"completion_tokens": outTok,
			"total_tokens":      inTok + outTok,
		},
	})
}

type chatWireChunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role             string         `json:"role"`
			Content          string         `json:"content"`
			ReasoningContent string         `json:"reasoning_content"`
			ToolCalls        []chatWireCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *chatWireUsage `json:"usage,omitempty"`
}

func (ChatCodec) DecodeStream(r io.Reader, emit func(StreamEvent) error) error {
	started := false
	sawError := false
	finishReason := FinishStop
	openTools := map[int]*ToolCall{}
	var toolOrder []int

	err := readSSEFrames(r, func(frame sseFrame) error {
		data := strings.TrimSpace(frame.Data)
		if data == "" || data == "[DONE]" {
			return nil
		}

		var evErr struct {
			Error *ErrorDetail `json:"error"`
		}

		if json.Unmarshal([]byte(data), &evErr) == nil && evErr.Error != nil && evErr.Error.Message != "" {
			sawError = true
			return emit(StreamEvent{Type: EventError, Error: evErr.Error})
		}

		var chunk chatWireChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return err
		}

		if !started && (chunk.ID != "" || chunk.Model != "" || len(chunk.Choices) > 0) {
			started = true

			if err := emit(StreamEvent{Type: EventMessageStart, ResponseID: chunk.ID, Model: chunk.Model, Role: "assistant"}); err != nil {
				return err
			}
		}

		if err := emitChatChoices(chunk, openTools, &toolOrder, &finishReason, emit); err != nil {
			return err
		}

		return emitChatChunkUsage(chunk.Usage, emit)
	})
	if err != nil {
		return err
	}

	for _, idx := range toolOrder {
		if err := emit(StreamEvent{Type: EventToolCallDone, Index: idx, ToolCall: openTools[idx]}); err != nil {
			return err
		}
	}

	if started && !sawError {
		return emit(StreamEvent{Type: EventMessageStop, FinishReason: finishReason})
	}

	return nil
}

func emitChatChoices(chunk chatWireChunk, openTools map[int]*ToolCall, toolOrder *[]int, finishReason *string, emit func(StreamEvent) error) error {
	for _, ch := range chunk.Choices {
		if ch.Delta.ReasoningContent != "" {
			if err := emit(StreamEvent{Type: EventThinkingDelta, Text: ch.Delta.ReasoningContent}); err != nil {
				return err
			}
		}

		if ch.Delta.Content != "" {
			if err := emit(StreamEvent{Type: EventTextDelta, Text: ch.Delta.Content}); err != nil {
				return err
			}
		}

		for i, tc := range ch.Delta.ToolCalls {
			if err := emitChatToolDelta(i, tc, openTools, toolOrder, emit); err != nil {
				return err
			}
		}

		if ch.FinishReason != nil && *ch.FinishReason != "" {
			*finishReason = *ch.FinishReason
		}
	}

	return nil
}

func emitChatToolDelta(i int, tc chatWireCall, openTools map[int]*ToolCall, toolOrder *[]int, emit func(StreamEvent) error) error {
	idx := i
	if tc.Index != nil {
		idx = *tc.Index
	}

	cur, exists := openTools[idx]
	if !exists {
		cur = &ToolCall{Index: idx, ID: tc.ID, Name: tc.Function.Name}
		openTools[idx] = cur

		*toolOrder = append(*toolOrder, idx)

		if err := emit(StreamEvent{Type: EventToolCallStart, Index: idx, ToolCall: cur}); err != nil {
			return err
		}
	} else {
		if tc.ID != "" && cur.ID == "" {
			cur.ID = tc.ID
		}

		if tc.Function.Name != "" && cur.Name == "" {
			cur.Name = tc.Function.Name
		}
	}

	if tc.Function.Arguments == "" {
		return nil
	}

	cur.Arguments += tc.Function.Arguments

	return emit(StreamEvent{
		Type:     EventToolCallDelta,
		Index:    idx,
		ToolCall: &ToolCall{Index: idx, ID: cur.ID, Arguments: tc.Function.Arguments},
	})
}

func emitChatChunkUsage(u *chatWireUsage, emit func(StreamEvent) error) error {
	if u == nil {
		return nil
	}

	var cached, reasoning int64
	if u.PromptTokensDetails != nil {
		cached = u.PromptTokensDetails.CachedTokens
	}

	if u.CompletionTokensDetails != nil {
		reasoning = u.CompletionTokensDetails.ReasoningTokens
	}

	return emit(StreamEvent{
		Type:  EventUsage,
		Usage: usageMeasures(u.PromptTokens, u.CompletionTokens, 0, cached, reasoning),
	})
}

type chatStreamEncoder struct {
	w          io.Writer
	id         string
	model      string
	emitUsage  bool
	sentRole   bool
	sentFinish bool
	closed     bool
	usage      []quota.Measure
}

func (ChatCodec) NewStreamEncoder(w io.Writer, req CanonicalRequest) StreamEncoder {
	emitUsage := true
	if req.SourceProtocol == ProtocolOpenAIChat && !req.ClientRequestedUsage {
		emitUsage = false
	}

	return &chatStreamEncoder{
		w:         w,
		id:        "chatcmpl-icp",
		model:     req.Model,
		emitUsage: emitUsage,
	}
}

func (e *chatStreamEncoder) writeChunk(payload map[string]any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(e.w, "data: %s\n\n", b)

	return err
}

func (e *chatStreamEncoder) WriteEvent(ev StreamEvent) error {
	switch ev.Type {
	case EventMessageStart:
		if ev.ResponseID != "" {
			e.id = ev.ResponseID
		}

		if ev.Model != "" {
			e.model = ev.Model
		}

		e.sentRole = true

		return e.writeChunk(map[string]any{
			"id":      e.id,
			"object":  "chat.completion.chunk",
			"model":   e.model,
			"choices": []map[string]any{{"index": 0, "delta": map[string]string{"role": "assistant"}}},
		})
	case EventThinkingDelta:
		if ev.Text == "" {
			return nil
		}

		return e.writeChunk(map[string]any{
			"id":      e.id,
			"object":  "chat.completion.chunk",
			"model":   e.model,
			"choices": []map[string]any{{"index": 0, "delta": map[string]string{"reasoning_content": ev.Text}}},
		})
	case EventTextDelta:
		delta := map[string]any{"content": ev.Text}

		if !e.sentRole {
			e.sentRole = true
			delta["role"] = "assistant"
		}

		return e.writeChunk(map[string]any{
			"id":      e.id,
			"object":  "chat.completion.chunk",
			"model":   e.model,
			"choices": []map[string]any{{"index": 0, "delta": delta}},
		})
	case EventToolCallStart:
		if ev.ToolCall == nil {
			return nil
		}

		idx := ev.ToolCall.Index

		return e.writeChunk(map[string]any{
			"id":     e.id,
			"object": "chat.completion.chunk",
			"model":  e.model,
			"choices": []map[string]any{{
				"index": 0,
				"delta": map[string]any{
					"tool_calls": []map[string]any{{
						"index":    idx,
						"id":       ev.ToolCall.ID,
						"type":     "function",
						"function": map[string]string{"name": ev.ToolCall.Name, "arguments": ""},
					}},
				},
			}},
		})
	case EventToolCallDelta:
		if ev.ToolCall == nil || ev.ToolCall.Arguments == "" {
			return nil
		}

		idx := ev.ToolCall.Index

		return e.writeChunk(map[string]any{
			"id":     e.id,
			"object": "chat.completion.chunk",
			"model":  e.model,
			"choices": []map[string]any{{
				"index": 0,
				"delta": map[string]any{
					"tool_calls": []map[string]any{{
						"index":    idx,
						"function": map[string]string{"arguments": ev.ToolCall.Arguments},
					}},
				},
			}},
		})
	case EventUsage:
		e.usage = append([]quota.Measure(nil), ev.Usage...)
	case EventMessageStop:
		reason := ev.FinishReason
		if reason == "" {
			reason = FinishStop
		}

		e.sentFinish = true
		if err := e.writeChunk(map[string]any{
			"id":      e.id,
			"object":  "chat.completion.chunk",
			"model":   e.model,
			"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": reason}},
		}); err != nil {
			return err
		}

		return e.Close()
	case EventError:
		if ev.Error != nil {
			return e.WriteError(fmt.Errorf("%s", ev.Error.Message))
		}
	}

	return nil
}

func (e *chatStreamEncoder) WriteError(err error) error {
	e.closed = true
	b, _ := json.Marshal(map[string]any{
		"error": map[string]string{"type": "server_error", "message": err.Error()},
	})
	_, wErr := fmt.Fprintf(e.w, "data: %s\n\n", b)

	return wErr
}

func (e *chatStreamEncoder) Close() error {
	if e.closed {
		return nil
	}

	e.closed = true
	if !e.sentFinish {
		if err := e.writeChunk(map[string]any{
			"id":      e.id,
			"object":  "chat.completion.chunk",
			"model":   e.model,
			"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
		}); err != nil {
			return err
		}
	}

	if e.emitUsage && len(e.usage) > 0 {
		inTok := measureVal(e.usage, "input_tokens")
		outTok := measureVal(e.usage, "output_tokens")

		if err := e.writeChunk(map[string]any{
			"id":      e.id,
			"object":  "chat.completion.chunk",
			"model":   e.model,
			"choices": []any{},
			"usage": map[string]int64{
				"prompt_tokens":     inTok,
				"completion_tokens": outTok,
				"total_tokens":      inTok + outTok,
			},
		}); err != nil {
			return err
		}
	}

	_, err := io.WriteString(e.w, "data: [DONE]\n\n")

	return err
}
