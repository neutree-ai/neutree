package protocol

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type AnthropicCodec struct{}

func (AnthropicCodec) Name() string {
	return ProtocolAnthropicMessages
}

func (AnthropicCodec) EndpointURL(baseURL string) string {
	return joinProtocolPath(baseURL, "/v1/messages", "/messages")
}

type anthropicWireReq struct {
	Model         string          `json:"model"`
	MaxTokens     int             `json:"max_tokens"`
	System        json.RawMessage `json:"system,omitempty"`
	Messages      []anthropicMsg  `json:"messages"`
	Tools         []anthropicTool `json:"tools,omitempty"`
	ToolChoice    json.RawMessage `json:"tool_choice,omitempty"`
	Temperature   *float64        `json:"temperature,omitempty"`
	TopP          *float64        `json:"top_p,omitempty"`
	StopSequences []string        `json:"stop_sequences,omitempty"`
	Stream        bool            `json:"stream,omitempty"`
	Thinking      *struct {
		Type         string `json:"type"`
		BudgetTokens int    `json:"budget_tokens"`
	} `json:"thinking,omitempty"`
}

type anthropicMsg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicTool struct {
	Type        string          `json:"type,omitempty"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

type anthropicBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Signature string          `json:"signature,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	Source    *struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type,omitempty"`
		Data      string `json:"data,omitempty"`
		URL       string `json:"url,omitempty"`
	} `json:"source,omitempty"`
}

func (AnthropicCodec) DecodeRequest(r io.Reader) (CanonicalRequest, error) {
	var wire anthropicWireReq
	if err := json.NewDecoder(r).Decode(&wire); err != nil {
		return CanonicalRequest{}, err
	}

	out := CanonicalRequest{
		SourceProtocol: ProtocolAnthropicMessages,
		Model:          wire.Model,
		System:         parseAnthropicBlocks(wire.System),
		ToolChoice:     ParseToolChoice(wire.ToolChoice),
		Temperature:    wire.Temperature,
		TopP:           wire.TopP,
		Stop:           wire.StopSequences,
		Stream:         wire.Stream,
	}

	if wire.MaxTokens > 0 {
		mt := wire.MaxTokens
		out.MaxTokens = &mt
	}

	if wire.Thinking != nil && wire.Thinking.BudgetTokens > 0 {
		bt := wire.Thinking.BudgetTokens
		out.ThinkingBudget = &bt
	}

	for _, t := range wire.Tools {
		if t.Type != "" && t.Type != "custom" {
			out.StatefulTools = append(out.StatefulTools, t.Type)
			continue
		}

		out.Tools = append(out.Tools, Tool{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.InputSchema,
		})
	}

	for _, m := range wire.Messages {
		out.Messages = append(out.Messages, Message{
			Role:    m.Role,
			Content: parseAnthropicBlocks(m.Content),
		})
	}

	return out, nil
}

func parseAnthropicBlocks(raw json.RawMessage) []ContentBlock {
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

	var list []anthropicBlock
	if json.Unmarshal(raw, &list) != nil {
		return nil
	}

	var blocks []ContentBlock

	for _, b := range list {
		switch b.Type {
		case BlockText:
			blocks = append(blocks, ContentBlock{Type: BlockText, Text: b.Text})
		case BlockThinking:
			blocks = append(blocks, ContentBlock{
				Type:            BlockThinking,
				Text:            b.Thinking,
				Signature:       b.Signature,
				SignatureOrigin: ProtocolAnthropicMessages,
			})
		case BlockImage:
			if b.Source != nil {
				blocks = append(blocks, ContentBlock{
					Type:      BlockImage,
					ImageURL:  b.Source.URL,
					MediaType: b.Source.MediaType,
					ImageData: b.Source.Data,
				})
			}
		case BlockToolUse:
			args := strings.TrimSpace(string(b.Input))
			if args == "" {
				args = "{}"
			}

			blocks = append(blocks, ContentBlock{
				Type:          BlockToolUse,
				ToolCallID:    b.ID,
				ToolName:      b.Name,
				ToolArguments: args,
			})
		case BlockToolResult:
			txt := joinBlockText(parseAnthropicBlocks(b.Content))
			blocks = append(blocks, ContentBlock{
				Type:       BlockToolResult,
				ToolCallID: b.ToolUseID,
				Text:       txt,
				IsError:    b.IsError,
			})
		}
	}

	return blocks
}

func (AnthropicCodec) EncodeRequest(req CanonicalRequest, targetModel string) ([]byte, error) {
	clean, err := SanitizeForTarget(req, targetModel, ProtocolAnthropicMessages)
	if err != nil {
		return nil, err
	}

	maxTok := 4096
	if clean.MaxTokens != nil && *clean.MaxTokens > 0 {
		maxTok = *clean.MaxTokens
	}

	if clean.ThinkingBudget != nil && *clean.ThinkingBudget >= maxTok {
		maxTok = *clean.ThinkingBudget + 1024
	}

	out := map[string]any{
		"model":      clean.Model,
		"max_tokens": maxTok,
	}

	if sysText := joinBlockText(clean.System); sysText != "" {
		out["system"] = sysText
	}

	type anthropicWireOutMsg struct {
		Role    string           `json:"role"`
		Content []map[string]any `json:"content"`
	}

	var msgs []anthropicWireOutMsg

	for _, m := range clean.Messages {
		encoded := encodeAnthropicContent(m.Content)
		if len(encoded) == 0 {
			continue
		}

		// Merge adjacent messages of the same role (e.g. multiple tool results).
		if n := len(msgs); n > 0 && msgs[n-1].Role == m.Role {
			msgs[n-1].Content = append(msgs[n-1].Content, encoded...)
			continue
		}

		msgs = append(msgs, anthropicWireOutMsg{
			Role:    m.Role,
			Content: encoded,
		})
	}

	out["messages"] = msgs

	if len(clean.Tools) > 0 {
		tools := make([]anthropicTool, 0, len(clean.Tools))

		for _, t := range clean.Tools {
			schema := t.Parameters
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}

			tools = append(tools, anthropicTool{
				Name:        t.Name,
				Description: t.Description,
				InputSchema: schema,
			})
		}

		out["tools"] = tools
	}

	if tc := encodeAnthropicToolChoice(clean.ToolChoice); tc != nil {
		out["tool_choice"] = tc
	}

	if clean.Temperature != nil {
		out["temperature"] = *clean.Temperature
	}

	if clean.TopP != nil {
		out["top_p"] = *clean.TopP
	}

	if len(clean.Stop) > 0 {
		out["stop_sequences"] = clean.Stop
	}

	if clean.ThinkingBudget != nil && *clean.ThinkingBudget > 0 {
		out["thinking"] = map[string]any{"type": "enabled", "budget_tokens": *clean.ThinkingBudget}
	}

	if clean.Stream {
		out["stream"] = true
	}

	return json.Marshal(out)
}

func encodeAnthropicToolChoice(tc *ToolChoice) map[string]string {
	if tc == nil || tc.Mode == "" {
		return nil
	}

	switch tc.Mode {
	case ToolChoiceAuto, ToolChoiceNone:
		return map[string]string{"type": tc.Mode}
	case ToolChoiceRequired:
		return map[string]string{"type": "any"}
	case ToolChoiceFunction:
		if tc.Name != "" {
			return map[string]string{"type": "tool", "name": tc.Name}
		}
	}

	return nil
}

func encodeAnthropicContent(blocks []ContentBlock) []map[string]any {
	var out []map[string]any

	for _, b := range blocks {
		switch b.Type {
		case BlockText:
			if b.Text != "" {
				out = append(out, map[string]any{"type": "text", "text": b.Text})
			}
		case BlockThinking:
			if b.Signature != "" {
				out = append(out, map[string]any{"type": "thinking", "thinking": b.Text, "signature": b.Signature})
			} else if b.Text != "" {
				// Foreign/unsigned reasoning cannot be sent as signed Anthropic thinking block without a valid signature.
				out = append(out, map[string]any{"type": "text", "text": b.Text})
			}
		case BlockImage:
			if b.ImageData != "" {
				mt := b.MediaType
				if mt == "" {
					mt = "image/png"
				}

				out = append(out, map[string]any{
					"type":   "image",
					"source": map[string]string{"type": "base64", "media_type": mt, "data": b.ImageData},
				})
			} else if b.ImageURL != "" {
				out = append(out, map[string]any{
					"type":   "image",
					"source": map[string]string{"type": "url", "url": b.ImageURL},
				})
			}
		case BlockToolUse:
			var inputObj any = map[string]any{}
			if b.ToolArguments != "" {
				_ = json.Unmarshal([]byte(b.ToolArguments), &inputObj)
			}

			out = append(out, map[string]any{
				"type":  "tool_use",
				"id":    b.ToolCallID,
				"name":  b.ToolName,
				"input": inputObj,
			})
		case BlockToolResult:
			out = append(out, map[string]any{
				"type":        "tool_result",
				"tool_use_id": b.ToolCallID,
				"content":     b.Text,
				"is_error":    b.IsError,
			})
		}
	}

	return out
}

type anthropicWireResp struct {
	ID         string          `json:"id"`
	Model      string          `json:"model"`
	Content    json.RawMessage `json:"content"`
	StopReason string          `json:"stop_reason"`
	Usage      struct {
		InputTokens              int64 `json:"input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

func mapAnthropicStopToIR(reason string) string {
	switch reason {
	case BlockToolUse:
		return FinishToolCalls
	case "max_tokens":
		return FinishLength
	default:
		return FinishStop
	}
}

func mapIRStopToAnthropic(reason string) string {
	switch reason {
	case FinishToolCalls:
		return BlockToolUse
	case FinishLength:
		return "max_tokens"
	default:
		return "end_turn"
	}
}

func (AnthropicCodec) DecodeResponse(r io.Reader) (CanonicalResponse, error) {
	var wire anthropicWireResp
	if err := json.NewDecoder(r).Decode(&wire); err != nil {
		return CanonicalResponse{}, err
	}

	totalInput := wire.Usage.InputTokens + wire.Usage.CacheCreationInputTokens + wire.Usage.CacheReadInputTokens

	return CanonicalResponse{
		ID:           wire.ID,
		Model:        wire.Model,
		Content:      parseAnthropicBlocks(wire.Content),
		FinishReason: mapAnthropicStopToIR(wire.StopReason),
		Usage:        usageMeasures(totalInput, wire.Usage.OutputTokens, wire.Usage.CacheCreationInputTokens, wire.Usage.CacheReadInputTokens, 0),
	}, nil
}

func (AnthropicCodec) EncodeResponse(resp CanonicalResponse, _ CanonicalRequest) ([]byte, error) {
	id := resp.ID
	if id == "" {
		id = "msg_icp"
	}

	content := encodeAnthropicContent(resp.Content)
	if content == nil {
		content = []map[string]any{}
	}

	return json.Marshal(map[string]any{
		"id":          id,
		"type":        "message",
		"role":        "assistant",
		"model":       resp.Model,
		"content":     content,
		"stop_reason": mapIRStopToAnthropic(resp.FinishReason),
		"usage": map[string]int64{
			"input_tokens":  measureVal(resp.Usage, "input_tokens"),
			"output_tokens": measureVal(resp.Usage, "output_tokens"),
		},
	})
}

func (AnthropicCodec) DecodeStream(r io.Reader, emit func(StreamEvent) error) error {
	var inputTok, outputTok, cacheCreate, cacheRead int64
	stopReason := FinishStop
	blockIsTool := map[int]*ToolCall{}
	toolSeq := 0

	err := readSSEFrames(r, func(frame sseFrame) error {
		if strings.TrimSpace(frame.Data) == "" {
			return nil
		}

		var ev struct {
			Type    string `json:"type"`
			Index   int    `json:"index"`
			Message struct {
				ID    string `json:"id"`
				Model string `json:"model"`
				Usage struct {
					InputTokens              int64 `json:"input_tokens"`
					OutputTokens             int64 `json:"output_tokens"`
					CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
					CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
			ContentBlock struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"content_block"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Thinking    string `json:"thinking"`
				Signature   string `json:"signature"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
			Usage struct {
				OutputTokens int64 `json:"output_tokens"`
			} `json:"usage"`
			Error *ErrorDetail `json:"error"`
		}

		if err := json.Unmarshal([]byte(frame.Data), &ev); err != nil {
			return err
		}

		kind := ev.Type
		if kind == "" {
			kind = frame.Event
		}

		switch kind {
		case "message_start":
			inputTok = ev.Message.Usage.InputTokens
			outputTok = ev.Message.Usage.OutputTokens
			cacheCreate = ev.Message.Usage.CacheCreationInputTokens
			cacheRead = ev.Message.Usage.CacheReadInputTokens

			return emit(StreamEvent{Type: EventMessageStart, ResponseID: ev.Message.ID, Model: ev.Message.Model, Role: "assistant"})
		case "content_block_start":
			if ev.ContentBlock.Type == BlockToolUse {
				tc := &ToolCall{Index: toolSeq, ID: ev.ContentBlock.ID, Name: ev.ContentBlock.Name}
				blockIsTool[ev.Index] = tc
				toolSeq++

				return emit(StreamEvent{Type: EventToolCallStart, Index: tc.Index, ToolCall: tc})
			}
		case "content_block_delta":
			switch ev.Delta.Type {
			case "text_delta":
				return emit(StreamEvent{Type: EventTextDelta, Index: ev.Index, Text: ev.Delta.Text})
			case "thinking_delta":
				return emit(StreamEvent{Type: EventThinkingDelta, Index: ev.Index, Text: ev.Delta.Thinking, Origin: ProtocolAnthropicMessages})
			case "signature_delta":
				return emit(StreamEvent{Type: EventThinkingDelta, Index: ev.Index, Signature: ev.Delta.Signature, Origin: ProtocolAnthropicMessages})
			case "input_json_delta":
				if tc, ok := blockIsTool[ev.Index]; ok {
					return emit(StreamEvent{
						Type:     EventToolCallDelta,
						Index:    tc.Index,
						ToolCall: &ToolCall{Index: tc.Index, ID: tc.ID, Arguments: ev.Delta.PartialJSON},
					})
				}
			}
		case "content_block_stop":
			if tc, ok := blockIsTool[ev.Index]; ok {
				return emit(StreamEvent{Type: EventToolCallDone, Index: tc.Index, ToolCall: tc})
			}
		case "message_delta":
			if ev.Usage.OutputTokens > 0 {
				outputTok = ev.Usage.OutputTokens
			}

			if ev.Delta.StopReason != "" {
				stopReason = mapAnthropicStopToIR(ev.Delta.StopReason)
			}

			totalInput := inputTok + cacheCreate + cacheRead
			if measures := usageMeasures(totalInput, outputTok, cacheCreate, cacheRead, 0); len(measures) > 0 {
				return emit(StreamEvent{Type: EventUsage, Usage: measures})
			}
		case "message_stop":
			return emit(StreamEvent{Type: EventMessageStop, FinishReason: stopReason})
		case "error":
			if ev.Error != nil {
				return emit(StreamEvent{Type: EventError, Error: ev.Error})
			}
		}

		return nil
	})

	return err
}

type anthropicStreamEncoder struct {
	w          io.Writer
	model      string
	started    bool
	closed     bool
	openBlock  int
	openKind   string
	nextIdx    int
	toolBlocks map[int]int
	inputTok   int64
	outputTok  int64
	stopReason string
}

func (AnthropicCodec) NewStreamEncoder(w io.Writer, req CanonicalRequest) StreamEncoder {
	return &anthropicStreamEncoder{
		w:          w,
		model:      req.Model,
		openBlock:  -1,
		toolBlocks: make(map[int]int),
		stopReason: "end_turn",
	}
}

func (e *anthropicStreamEncoder) writeSSE(event string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(e.w, "event: %s\ndata: %s\n\n", event, b)

	return err
}

func (e *anthropicStreamEncoder) ensureStarted(id, model string) error {
	if e.started {
		return nil
	}

	e.started = true

	if id == "" {
		id = "msg_icp"
	}

	if model == "" {
		model = e.model
	}

	return e.writeSSE("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":      id,
			"type":    "message",
			"role":    "assistant",
			"model":   model,
			"content": []any{},
			"usage":   map[string]int64{"input_tokens": e.inputTok, "output_tokens": 0},
		},
	})
}

func (e *anthropicStreamEncoder) closeOpenBlock() error {
	if e.openBlock < 0 {
		return nil
	}

	idx := e.openBlock
	e.openBlock = -1
	e.openKind = ""

	return e.writeSSE("content_block_stop", map[string]any{"type": "content_block_stop", "index": idx})
}

func (e *anthropicStreamEncoder) WriteEvent(ev StreamEvent) error {
	switch ev.Type {
	case EventMessageStart:
		return e.ensureStarted(ev.ResponseID, ev.Model)
	case EventThinkingDelta:
		if err := e.ensureStarted("", ""); err != nil {
			return err
		}

		if e.openKind != BlockThinking {
			if err := e.closeOpenBlock(); err != nil {
				return err
			}

			e.openBlock = e.nextIdx
			e.nextIdx++
			e.openKind = BlockThinking

			if err := e.writeSSE("content_block_start", map[string]any{
				"type":          "content_block_start",
				"index":         e.openBlock,
				"content_block": map[string]string{"type": BlockThinking, "thinking": ""},
			}); err != nil {
				return err
			}
		}

		if ev.Text != "" {
			if err := e.writeSSE("content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": e.openBlock,
				"delta": map[string]string{"type": "thinking_delta", "thinking": ev.Text},
			}); err != nil {
				return err
			}
		}

		if ev.Signature != "" && ev.Origin == ProtocolAnthropicMessages {
			return e.writeSSE("content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": e.openBlock,
				"delta": map[string]string{"type": "signature_delta", "signature": ev.Signature},
			})
		}
	case EventTextDelta:
		if err := e.ensureStarted("", ""); err != nil {
			return err
		}

		if e.openKind != BlockText {
			if err := e.closeOpenBlock(); err != nil {
				return err
			}

			e.openBlock = e.nextIdx
			e.nextIdx++
			e.openKind = BlockText

			if err := e.writeSSE("content_block_start", map[string]any{
				"type":          "content_block_start",
				"index":         e.openBlock,
				"content_block": map[string]string{"type": BlockText, "text": ""},
			}); err != nil {
				return err
			}
		}

		return e.writeSSE("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": e.openBlock,
			"delta": map[string]string{"type": "text_delta", "text": ev.Text},
		})
	case EventToolCallStart:
		if ev.ToolCall == nil {
			return nil
		}

		if err := e.ensureStarted("", ""); err != nil {
			return err
		}

		if err := e.closeOpenBlock(); err != nil {
			return err
		}

		blkIdx := e.nextIdx
		e.nextIdx++
		e.openBlock = blkIdx
		e.openKind = "tool_use"
		e.toolBlocks[ev.ToolCall.Index] = blkIdx

		return e.writeSSE("content_block_start", map[string]any{
			"type":  "content_block_start",
			"index": blkIdx,
			"content_block": map[string]any{
				"type":  "tool_use",
				"id":    ev.ToolCall.ID,
				"name":  ev.ToolCall.Name,
				"input": map[string]any{},
			},
		})
	case EventToolCallDelta:
		if ev.ToolCall == nil || ev.ToolCall.Arguments == "" {
			return nil
		}

		blkIdx, ok := e.toolBlocks[ev.ToolCall.Index]
		if !ok {
			blkIdx = e.openBlock
		}

		return e.writeSSE("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": blkIdx,
			"delta": map[string]string{"type": "input_json_delta", "partial_json": ev.ToolCall.Arguments},
		})
	case EventToolCallDone:
		e.stopReason = "tool_use"
		return e.closeOpenBlock()
	case EventUsage:
		e.inputTok = measureVal(ev.Usage, "input_tokens")
		e.outputTok = measureVal(ev.Usage, "output_tokens")
	case EventMessageStop:
		if ev.FinishReason != "" {
			e.stopReason = mapIRStopToAnthropic(ev.FinishReason)
		}

		return e.Close()
	case EventError:
		if ev.Error != nil {
			return e.WriteError(fmt.Errorf("%s", ev.Error.Message))
		}
	}

	return nil
}

func (e *anthropicStreamEncoder) WriteError(err error) error {
	e.closed = true

	return e.writeSSE("error", map[string]any{
		"type":  "error",
		"error": map[string]string{"type": "api_error", "message": err.Error()},
	})
}

func (e *anthropicStreamEncoder) Close() error {
	if e.closed {
		return nil
	}

	e.closed = true
	if err := e.ensureStarted("", ""); err != nil {
		return err
	}

	if err := e.closeOpenBlock(); err != nil {
		return err
	}

	if err := e.writeSSE("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]string{"stop_reason": e.stopReason},
		"usage": map[string]int64{"input_tokens": e.inputTok, "output_tokens": e.outputTok},
	}); err != nil {
		return err
	}

	return e.writeSSE("message_stop", map[string]string{"type": "message_stop"})
}
