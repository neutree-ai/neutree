package protocol

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/neutree-ai/neutree/internal/icp/quota"
)

type ResponsesCodec struct{}

func (ResponsesCodec) Name() string {
	return ProtocolOpenAIResponses
}

func (ResponsesCodec) EndpointURL(baseURL string) string {
	return joinProtocolPath(baseURL, "/v1/responses", "/responses")
}

type responsesWireReq struct {
	Model              string          `json:"model"`
	Instructions       string          `json:"instructions,omitempty"`
	Input              json.RawMessage `json:"input"`
	Tools              []responsesTool `json:"tools,omitempty"`
	ToolChoice         json.RawMessage `json:"tool_choice,omitempty"`
	MaxOutputTokens    *int            `json:"max_output_tokens,omitempty"`
	Temperature        *float64        `json:"temperature,omitempty"`
	TopP               *float64        `json:"top_p,omitempty"`
	Stream             bool            `json:"stream,omitempty"`
	PreviousResponseID string          `json:"previous_response_id,omitempty"`
	Store              bool            `json:"store,omitempty"`
	Reasoning          *struct {
		Effort string `json:"effort,omitempty"`
	} `json:"reasoning,omitempty"`
}

type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Function    *struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
	} `json:"function,omitempty"`
}

type responsesItem struct {
	Type             string          `json:"type,omitempty"`
	ID               string          `json:"id,omitempty"`
	Role             string          `json:"role,omitempty"`
	Content          json.RawMessage `json:"content,omitempty"`
	CallID           string          `json:"call_id,omitempty"`
	Name             string          `json:"name,omitempty"`
	Arguments        string          `json:"arguments,omitempty"`
	Output           string          `json:"output,omitempty"`
	EncryptedContent string          `json:"encrypted_content,omitempty"`
	Summary          []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"summary,omitempty"`
}

func (ResponsesCodec) DecodeRequest(r io.Reader) (CanonicalRequest, error) {
	var wire responsesWireReq
	if err := json.NewDecoder(r).Decode(&wire); err != nil {
		return CanonicalRequest{}, err
	}

	out := CanonicalRequest{
		SourceProtocol:     ProtocolOpenAIResponses,
		Model:              wire.Model,
		ToolChoice:         ParseToolChoice(wire.ToolChoice),
		MaxTokens:          wire.MaxOutputTokens,
		Temperature:        wire.Temperature,
		TopP:               wire.TopP,
		Stream:             wire.Stream,
		PreviousResponseID: wire.PreviousResponseID,
		Store:              wire.Store,
	}

	if wire.Instructions != "" {
		out.System = []ContentBlock{{Type: BlockText, Text: wire.Instructions}}
	}

	if wire.Reasoning != nil {
		out.ReasoningEffort = wire.Reasoning.Effort
	}

	for _, t := range wire.Tools {
		if t.Type != "" && t.Type != "function" {
			out.StatefulTools = append(out.StatefulTools, t.Type)
			continue
		}

		name, desc, params := t.Name, t.Description, t.Parameters
		if t.Function != nil && name == "" {
			name, desc, params = t.Function.Name, t.Function.Description, t.Function.Parameters
		}

		out.Tools = append(out.Tools, Tool{Name: name, Description: desc, Parameters: params})
	}

	sysExtra, msgs := parseResponsesInput(wire.Input)
	out.System = append(out.System, sysExtra...)
	out.Messages = msgs

	return out, nil
}

func parseResponsesInput(raw json.RawMessage) ([]ContentBlock, []Message) {
	if len(raw) == 0 || string(raw) == jsonNullLiteral {
		return nil, nil
	}

	var str string
	if json.Unmarshal(raw, &str) == nil {
		return nil, []Message{{Role: "user", Content: []ContentBlock{{Type: BlockText, Text: str}}}}
	}

	var items []responsesItem
	if json.Unmarshal(raw, &items) != nil {
		return nil, nil
	}

	var sys []ContentBlock
	var msgs []Message

	for _, it := range items {
		switch it.Type {
		case itemFunctionCall:
			cid := it.CallID
			if cid == "" {
				cid = it.ID
			}

			msgs = append(msgs, Message{
				Role: "assistant",
				Content: []ContentBlock{{
					Type:          BlockToolUse,
					ToolCallID:    cid,
					ToolName:      it.Name,
					ToolArguments: it.Arguments,
				}},
			})
		case "function_call_output":
			msgs = append(msgs, Message{
				Role: "user",
				Content: []ContentBlock{{
					Type:       BlockToolResult,
					ToolCallID: it.CallID,
					Text:       it.Output,
				}},
			})
		case itemReasoning:
			var txt strings.Builder
			for _, s := range it.Summary {
				txt.WriteString(s.Text)
			}

			msgs = append(msgs, Message{
				Role: "assistant",
				Content: []ContentBlock{{
					Type:             BlockThinking,
					Text:             txt.String(),
					EncryptedContent: it.EncryptedContent,
					SignatureOrigin:  ProtocolOpenAIResponses,
				}},
			})
		default:
			blocks := parseResponsesContent(it.Content)
			if it.Role == "system" || it.Role == "developer" {
				sys = append(sys, blocks...)
				continue
			}

			role := it.Role
			if role == "" {
				role = "user"
			}

			msgs = append(msgs, Message{Role: role, Content: blocks})
		}
	}

	return sys, msgs
}

func parseResponsesContent(raw json.RawMessage) []ContentBlock {
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
		ImageURL string `json:"image_url"`
	}

	if json.Unmarshal(raw, &parts) != nil {
		return nil
	}

	var out []ContentBlock

	for _, p := range parts {
		switch p.Type {
		case "input_text", "output_text", "text":
			out = append(out, ContentBlock{Type: BlockText, Text: p.Text})
		case "input_image":
			out = append(out, ContentBlock{Type: BlockImage, ImageURL: p.ImageURL})
		}
	}

	return out
}

func (ResponsesCodec) EncodeRequest(req CanonicalRequest, targetModel string) ([]byte, error) {
	clean, err := SanitizeForTarget(req, targetModel, ProtocolOpenAIResponses)
	if err != nil {
		return nil, err
	}

	out := map[string]any{
		"model": clean.Model,
	}

	if sysText := joinBlockText(clean.System); sysText != "" {
		out["instructions"] = sysText
	}

	var inputItems []map[string]any
	for _, m := range clean.Messages {
		inputItems = append(inputItems, encodeResponsesItems(m)...)
	}

	out["input"] = inputItems

	var tools []map[string]any

	for _, t := range clean.Tools {
		params := t.Parameters
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}

		tools = append(tools, map[string]any{
			"type":        "function",
			"name":        t.Name,
			"description": t.Description,
			"parameters":  params,
		})
	}

	for _, st := range clean.StatefulTools {
		tools = append(tools, map[string]any{"type": st})
	}

	if len(tools) > 0 {
		out["tools"] = tools
	}

	if tc := encodeResponsesToolChoice(clean.ToolChoice); tc != nil {
		out["tool_choice"] = tc
	}

	if clean.MaxTokens != nil {
		out["max_output_tokens"] = *clean.MaxTokens
	}

	if clean.Temperature != nil {
		out["temperature"] = *clean.Temperature
	}

	if clean.TopP != nil {
		out["top_p"] = *clean.TopP
	}

	if clean.PreviousResponseID != "" {
		out["previous_response_id"] = clean.PreviousResponseID
	}

	if clean.Store {
		out["store"] = true
	}

	if clean.ReasoningEffort != "" {
		out["reasoning"] = map[string]string{"effort": clean.ReasoningEffort}
	}

	if clean.Stream {
		out["stream"] = true
	}

	return json.Marshal(out)
}

func encodeResponsesToolChoice(tc *ToolChoice) any {
	if tc == nil || tc.Mode == "" {
		return nil
	}

	switch tc.Mode {
	case ToolChoiceAuto, ToolChoiceNone, ToolChoiceRequired:
		return tc.Mode
	case ToolChoiceFunction:
		if tc.Name != "" {
			return map[string]string{"type": itemFunctionCall, "name": tc.Name}
		}
	}

	return nil
}

func encodeResponsesItems(m Message) []map[string]any {
	var items []map[string]any
	var msgParts []map[string]any

	partType := "input_text"
	if m.Role == "assistant" {
		partType = "output_text"
	}

	flushMsg := func() {
		if len(msgParts) == 0 {
			return
		}

		items = append(items, map[string]any{
			"type":    "message",
			"role":    m.Role,
			"content": msgParts,
		})
		msgParts = nil
	}

	for _, b := range m.Content {
		switch b.Type {
		case BlockText:
			msgParts = append(msgParts, map[string]any{"type": partType, "text": b.Text})
		case BlockImage:
			msgParts = append(msgParts, map[string]any{"type": "input_image", "image_url": b.ImageURL})
		case BlockThinking:
			flushMsg()

			item := map[string]any{
				"type":    itemReasoning,
				"summary": []map[string]string{{"type": "summary_text", "text": b.Text}},
			}

			if b.EncryptedContent != "" {
				item["encrypted_content"] = b.EncryptedContent
			}

			items = append(items, item)
		case BlockToolUse:
			flushMsg()

			args := b.ToolArguments
			if args == "" {
				args = "{}"
			}

			items = append(items, map[string]any{
				"type":      itemFunctionCall,
				"call_id":   b.ToolCallID,
				"name":      b.ToolName,
				"arguments": args,
			})
		case BlockToolResult:
			flushMsg()

			items = append(items, map[string]any{
				"type":    "function_call_output",
				"call_id": b.ToolCallID,
				"output":  b.Text,
			})
		}
	}

	flushMsg()

	return items
}

type responsesWireResp struct {
	ID     string          `json:"id"`
	Model  string          `json:"model"`
	Status string          `json:"status"`
	Output []responsesItem `json:"output"`
	Usage  *struct {
		InputTokens        int64 `json:"input_tokens"`
		OutputTokens       int64 `json:"output_tokens"`
		InputTokensDetails *struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"input_tokens_details,omitempty"`
		OutputTokensDetails *struct {
			ReasoningTokens int64 `json:"reasoning_tokens"`
		} `json:"output_tokens_details,omitempty"`
	} `json:"usage,omitempty"`
}

func (ResponsesCodec) DecodeResponse(r io.Reader) (CanonicalResponse, error) {
	var wire responsesWireResp
	if err := json.NewDecoder(r).Decode(&wire); err != nil {
		return CanonicalResponse{}, err
	}

	return decodeResponsesObject(wire), nil
}

func decodeResponsesObject(wire responsesWireResp) CanonicalResponse {
	resp := CanonicalResponse{
		ID:           wire.ID,
		Model:        wire.Model,
		FinishReason: FinishStop,
	}

	hasTool := false

	for _, it := range wire.Output {
		switch it.Type {
		case itemReasoning:
			var txt strings.Builder

			for _, s := range it.Summary {
				txt.WriteString(s.Text)
			}

			resp.Content = append(resp.Content, ContentBlock{
				Type:             BlockThinking,
				Text:             txt.String(),
				EncryptedContent: it.EncryptedContent,
				SignatureOrigin:  ProtocolOpenAIResponses,
			})
		case itemFunctionCall:
			hasTool = true

			cid := it.CallID
			if cid == "" {
				cid = it.ID
			}

			resp.Content = append(resp.Content, ContentBlock{
				Type:          BlockToolUse,
				ToolCallID:    cid,
				ToolName:      it.Name,
				ToolArguments: it.Arguments,
			})
		default:
			resp.Content = append(resp.Content, parseResponsesContent(it.Content)...)
		}
	}

	if hasTool {
		resp.FinishReason = FinishToolCalls
	} else if wire.Status == respStatusIncomplete {
		resp.FinishReason = FinishLength
	}

	if wire.Usage != nil {
		var cached, reasoning int64
		if wire.Usage.InputTokensDetails != nil {
			cached = wire.Usage.InputTokensDetails.CachedTokens
		}

		if wire.Usage.OutputTokensDetails != nil {
			reasoning = wire.Usage.OutputTokensDetails.ReasoningTokens
		}

		resp.Usage = usageMeasures(wire.Usage.InputTokens, wire.Usage.OutputTokens, 0, cached, reasoning)
	}

	return resp
}

func (ResponsesCodec) EncodeResponse(resp CanonicalResponse, _ CanonicalRequest) ([]byte, error) {
	return json.Marshal(buildResponsesWireObject(resp))
}

func buildResponsesWireObject(resp CanonicalResponse) map[string]any {
	id := resp.ID
	if id == "" {
		id = "resp_icp"
	}

	var output []map[string]any
	var textParts []map[string]any

	flushMsg := func() {
		if len(textParts) == 0 {
			return
		}

		output = append(output, map[string]any{
			"id":      "msg_" + id,
			"type":    "message",
			"role":    "assistant",
			"status":  "completed",
			"content": textParts,
		})
		textParts = nil
	}

	for i, b := range resp.Content {
		switch b.Type {
		case BlockThinking:
			flushMsg()

			item := map[string]any{
				"id":      fmt.Sprintf("rs_%d", i),
				"type":    itemReasoning,
				"summary": []map[string]string{{"type": "summary_text", "text": b.Text}},
			}

			if b.EncryptedContent != "" && b.SignatureOrigin == ProtocolOpenAIResponses {
				item["encrypted_content"] = b.EncryptedContent
			}

			output = append(output, item)
		case BlockText:
			textParts = append(textParts, map[string]any{"type": "output_text", "text": b.Text})
		case BlockToolUse:
			flushMsg()

			cid := b.ToolCallID
			if cid == "" {
				cid = fmt.Sprintf("call_%d", i)
			}

			output = append(output, map[string]any{
				"id":        "fc_" + cid,
				"type":      itemFunctionCall,
				"status":    "completed",
				"call_id":   cid,
				"name":      b.ToolName,
				"arguments": b.ToolArguments,
			})
		}
	}

	flushMsg()

	inTok := measureVal(resp.Usage, "input_tokens")
	outTok := measureVal(resp.Usage, "output_tokens")

	status := respStatusCompleted
	if resp.FinishReason == FinishLength {
		status = respStatusIncomplete
	}

	return map[string]any{
		"id":     id,
		"object": "response",
		"status": status,
		"model":  resp.Model,
		"output": output,
		"usage": map[string]int64{
			"input_tokens":  inTok,
			"output_tokens": outTok,
			"total_tokens":  inTok + outTok,
		},
	}
}

func (ResponsesCodec) DecodeStream(r io.Reader, emit func(StreamEvent) error) error {
	toolByItem := map[int]*ToolCall{}
	toolSeq := 0
	hasTool := false

	return readSSEFrames(r, func(frame sseFrame) error {
		data := strings.TrimSpace(frame.Data)
		if data == "" || data == "[DONE]" {
			return nil
		}

		var ev struct {
			Type        string             `json:"type"`
			OutputIndex int                `json:"output_index"`
			Delta       string             `json:"delta"`
			Arguments   string             `json:"arguments"`
			Item        responsesItem      `json:"item"`
			Response    *responsesWireResp `json:"response"`
			Error       *ErrorDetail       `json:"error"`
		}

		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return err
		}

		kind := ev.Type
		if kind == "" {
			kind = frame.Event
		}

		switch kind {
		case "response.created":
			var id, model string
			if ev.Response != nil {
				id, model = ev.Response.ID, ev.Response.Model
			}

			return emit(StreamEvent{Type: EventMessageStart, ResponseID: id, Model: model, Role: "assistant"})
		case "response.output_item.added":
			if ev.Item.Type == itemFunctionCall {
				hasTool = true

				cid := ev.Item.CallID
				if cid == "" {
					cid = ev.Item.ID
				}

				tc := &ToolCall{Index: toolSeq, ID: cid, Name: ev.Item.Name}
				toolByItem[ev.OutputIndex] = tc
				toolSeq++

				return emit(StreamEvent{Type: EventToolCallStart, Index: tc.Index, ToolCall: tc})
			}
		case "response.output_text.delta":
			return emit(StreamEvent{Type: EventTextDelta, Index: ev.OutputIndex, Text: ev.Delta})
		case "response.reasoning_summary_text.delta":
			return emit(StreamEvent{Type: EventThinkingDelta, Index: ev.OutputIndex, Text: ev.Delta, Origin: ProtocolOpenAIResponses})
		case "response.function_call_arguments.delta":
			if tc, ok := toolByItem[ev.OutputIndex]; ok {
				return emit(StreamEvent{
					Type:     EventToolCallDelta,
					Index:    tc.Index,
					ToolCall: &ToolCall{Index: tc.Index, ID: tc.ID, Arguments: ev.Delta},
				})
			}
		case "response.output_item.done":
			if ev.Item.Type == itemReasoning && ev.Item.EncryptedContent != "" {
				return emit(StreamEvent{
					Type:      EventThinkingDelta,
					Index:     ev.OutputIndex,
					Encrypted: ev.Item.EncryptedContent,
					Origin:    ProtocolOpenAIResponses,
				})
			}

			if tc, ok := toolByItem[ev.OutputIndex]; ok {
				return emit(StreamEvent{
					Type:     EventToolCallDone,
					Index:    tc.Index,
					ToolCall: &ToolCall{Index: tc.Index, ID: tc.ID, Name: tc.Name, Arguments: ev.Item.Arguments},
				})
			}
		case "response.completed":
			if ev.Response != nil {
				decoded := decodeResponsesObject(*ev.Response)
				if len(decoded.Usage) > 0 {
					if err := emit(StreamEvent{Type: EventUsage, Usage: decoded.Usage}); err != nil {
						return err
					}
				}
			}

			reason := FinishStop
			if hasTool {
				reason = FinishToolCalls
			}

			return emit(StreamEvent{Type: EventMessageStop, FinishReason: reason})
		case "error", "response.failed":
			if ev.Error != nil {
				return emit(StreamEvent{Type: EventError, Error: ev.Error})
			}
		}

		return nil
	})
}

type responsesToolStreamState struct {
	outIdx int
	id     string
	name   string
	args   strings.Builder
	done   bool
}

type responsesStreamEncoder struct {
	w               io.Writer
	model           string
	respID          string
	started         bool
	closed          bool
	reasonOpened    bool
	reasonOutIdx    int
	reasonBuf       strings.Builder
	reasonEncrypted string
	reasonOrigin    string
	msgOpened       bool
	msgOutIdx       int
	msgBuf          strings.Builder
	nextOutIdx      int
	openTools       map[int]*responsesToolStreamState
	completedOutput []map[string]any
	usage           []quota.Measure
	finishReason    string
}

func (ResponsesCodec) NewStreamEncoder(w io.Writer, req CanonicalRequest) StreamEncoder {
	return &responsesStreamEncoder{
		w:            w,
		model:        req.Model,
		respID:       "resp_icp",
		openTools:    make(map[int]*responsesToolStreamState),
		finishReason: FinishStop,
	}
}

func (e *responsesStreamEncoder) writeSSE(evType string, payload map[string]any) error {
	payload["type"] = evType

	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(e.w, "event: %s\ndata: %s\n\n", evType, b)

	return err
}

func (e *responsesStreamEncoder) ensureStarted() error {
	if e.started {
		return nil
	}

	e.started = true

	return e.writeSSE("response.created", map[string]any{
		"response": map[string]any{
			"id":     e.respID,
			"object": "response",
			"status": "in_progress",
			"model":  e.model,
			"output": []any{},
		},
	})
}

func (e *responsesStreamEncoder) closeReasonItem() error {
	if !e.reasonOpened {
		return nil
	}

	e.reasonOpened = false
	item := map[string]any{
		"id":      fmt.Sprintf("rs_%d", e.reasonOutIdx),
		"type":    itemReasoning,
		"summary": []map[string]string{{"type": "summary_text", "text": e.reasonBuf.String()}},
	}

	if e.reasonEncrypted != "" && e.reasonOrigin == ProtocolOpenAIResponses {
		item["encrypted_content"] = e.reasonEncrypted
	}

	e.reasonBuf.Reset()
	e.reasonEncrypted = ""
	e.reasonOrigin = ""
	e.completedOutput = append(e.completedOutput, item)

	return e.writeSSE("response.output_item.done", map[string]any{
		"output_index": e.reasonOutIdx,
		"item":         item,
	})
}

func (e *responsesStreamEncoder) closeMsgItem() error {
	if !e.msgOpened {
		return nil
	}

	e.msgOpened = false
	txt := e.msgBuf.String()
	e.msgBuf.Reset()

	item := map[string]any{
		"id":      fmt.Sprintf("msg_%s_%d", e.respID, e.msgOutIdx),
		"type":    "message",
		"role":    "assistant",
		"status":  "completed",
		"content": []map[string]string{{"type": "output_text", "text": txt}},
	}
	e.completedOutput = append(e.completedOutput, item)

	return e.writeSSE("response.output_item.done", map[string]any{
		"output_index": e.msgOutIdx,
		"item":         item,
	})
}

func (e *responsesStreamEncoder) WriteEvent(ev StreamEvent) error {
	switch ev.Type {
	case EventMessageStart:
		if ev.ResponseID != "" {
			e.respID = ev.ResponseID
		}

		if ev.Model != "" {
			e.model = ev.Model
		}

		return e.ensureStarted()
	case EventThinkingDelta:
		return e.writeThinkingDelta(ev)
	case EventTextDelta:
		return e.writeTextDelta(ev.Text)
	case EventToolCallStart:
		return e.writeToolCallStart(ev.ToolCall)
	case EventToolCallDelta:
		return e.writeToolCallDelta(ev.ToolCall)
	case EventToolCallDone:
		return e.writeToolCallDone(ev.ToolCall)
	case EventUsage:
		e.usage = append([]quota.Measure(nil), ev.Usage...)
	case EventMessageStop:
		if ev.FinishReason != "" {
			e.finishReason = ev.FinishReason
		}

		return e.Close()
	case EventError:
		if ev.Error != nil {
			return e.WriteError(fmt.Errorf("%s", ev.Error.Message))
		}
	}

	return nil
}

func (e *responsesStreamEncoder) writeThinkingDelta(ev StreamEvent) error {
	if err := e.ensureStarted(); err != nil {
		return err
	}

	if !e.reasonOpened {
		if err := e.closeMsgItem(); err != nil {
			return err
		}

		e.reasonOpened = true
		e.reasonOutIdx = e.nextOutIdx
		e.nextOutIdx++

		if err := e.writeSSE("response.output_item.added", map[string]any{
			"output_index": e.reasonOutIdx,
			"item": map[string]any{
				"id":      fmt.Sprintf("rs_%d", e.reasonOutIdx),
				"type":    itemReasoning,
				"summary": []any{},
			},
		}); err != nil {
			return err
		}
	}

	if ev.Encrypted != "" {
		e.reasonEncrypted = ev.Encrypted
	}

	if ev.Origin != "" {
		e.reasonOrigin = ev.Origin
	}

	if ev.Text == "" {
		return nil
	}

	e.reasonBuf.WriteString(ev.Text)

	return e.writeSSE("response.reasoning_summary_text.delta", map[string]any{
		"output_index": e.reasonOutIdx,
		"delta":        ev.Text,
	})
}

func (e *responsesStreamEncoder) writeTextDelta(text string) error {
	if err := e.ensureStarted(); err != nil {
		return err
	}

	if !e.msgOpened {
		if err := e.closeReasonItem(); err != nil {
			return err
		}

		e.msgOpened = true
		e.msgOutIdx = e.nextOutIdx
		e.nextOutIdx++

		if err := e.writeSSE("response.output_item.added", map[string]any{
			"output_index": e.msgOutIdx,
			"item": map[string]any{
				"id":      fmt.Sprintf("msg_%s_%d", e.respID, e.msgOutIdx),
				"type":    "message",
				"role":    "assistant",
				"status":  "in_progress",
				"content": []any{},
			},
		}); err != nil {
			return err
		}

		if err := e.writeSSE("response.content_part.added", map[string]any{
			"output_index":  e.msgOutIdx,
			"content_index": 0,
			"part":          map[string]string{"type": "output_text", "text": ""},
		}); err != nil {
			return err
		}
	}

	e.msgBuf.WriteString(text)

	return e.writeSSE("response.output_text.delta", map[string]any{
		"output_index":  e.msgOutIdx,
		"content_index": 0,
		"delta":         text,
	})
}

func (e *responsesStreamEncoder) writeToolCallStart(tc *ToolCall) error {
	if tc == nil {
		return nil
	}

	if err := e.ensureStarted(); err != nil {
		return err
	}

	if err := e.closeReasonItem(); err != nil {
		return err
	}

	if err := e.closeMsgItem(); err != nil {
		return err
	}

	outIdx := e.nextOutIdx
	e.nextOutIdx++
	st := &responsesToolStreamState{outIdx: outIdx, id: tc.ID, name: tc.Name}
	e.openTools[tc.Index] = st

	return e.writeSSE("response.output_item.added", map[string]any{
		"output_index": outIdx,
		"item": map[string]any{
			"id":        "fc_" + tc.ID,
			"type":      itemFunctionCall,
			"status":    "in_progress",
			"call_id":   tc.ID,
			"name":      tc.Name,
			"arguments": "",
		},
	})
}

func (e *responsesStreamEncoder) writeToolCallDelta(tc *ToolCall) error {
	if tc == nil || tc.Arguments == "" {
		return nil
	}

	st, ok := e.openTools[tc.Index]
	if !ok {
		return nil
	}

	st.args.WriteString(tc.Arguments)

	return e.writeSSE("response.function_call_arguments.delta", map[string]any{
		"output_index": st.outIdx,
		"delta":        tc.Arguments,
	})
}

func (e *responsesStreamEncoder) writeToolCallDone(tc *ToolCall) error {
	if tc == nil {
		return nil
	}

	st, ok := e.openTools[tc.Index]
	if !ok || st.done {
		return nil
	}

	st.done = true

	args := st.args.String()
	if args == "" {
		args = tc.Arguments
	}

	id := st.id
	if id == "" {
		id = tc.ID
	}

	name := st.name
	if name == "" {
		name = tc.Name
	}

	item := map[string]any{
		"id":        "fc_" + id,
		"type":      itemFunctionCall,
		"status":    "completed",
		"call_id":   id,
		"name":      name,
		"arguments": args,
	}
	e.completedOutput = append(e.completedOutput, item)

	return e.writeSSE("response.output_item.done", map[string]any{
		"output_index": st.outIdx,
		"item":         item,
	})
}

func (e *responsesStreamEncoder) WriteError(err error) error {
	e.closed = true

	return e.writeSSE("error", map[string]any{
		"error": map[string]string{"type": "server_error", "message": err.Error()},
	})
}

func (e *responsesStreamEncoder) Close() error {
	if e.closed {
		return nil
	}

	e.closed = true
	if err := e.ensureStarted(); err != nil {
		return err
	}

	if err := e.closeReasonItem(); err != nil {
		return err
	}

	if err := e.closeMsgItem(); err != nil {
		return err
	}

	inTok := measureVal(e.usage, "input_tokens")
	outTok := measureVal(e.usage, "output_tokens")

	status := respStatusCompleted
	if e.finishReason == FinishLength {
		status = respStatusIncomplete
	}

	out := e.completedOutput
	if out == nil {
		out = []map[string]any{}
	}

	return e.writeSSE("response.completed", map[string]any{
		"response": map[string]any{
			"id":     e.respID,
			"object": "response",
			"status": status,
			"model":  e.model,
			"output": out,
			"usage": map[string]int64{
				"input_tokens":  inTok,
				"output_tokens": outTok,
				"total_tokens":  inTok + outTok,
			},
		},
	})
}
