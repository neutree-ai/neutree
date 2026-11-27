package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
)

const (
	evMeta     = "meta"
	evBlock    = "block"
	evBlockEnd = "block_end"
	evEnd      = "end"
	evError    = "error"
	chatDone   = "[DONE]"
)

type streamDecoder struct {
	wire    ID
	meta    Event
	ended   bool
	sawTool bool
	// Responses indexes parts within an item. Chat indexes only tool calls.
	items           map[int]*decodedItem
	next, chatIndex int
	chatKind        string
}

type decodedItem struct {
	parts   map[int]int
	hasArgs bool
}

func (c wireCodec) NewStreamDecoder() StreamDecoder {
	return &streamDecoder{wire: ID(c), meta: Event{Kind: evMeta}, items: make(map[int]*decodedItem)}
}

func (d *streamDecoder) Finish() error {
	if !d.ended {
		return io.ErrUnexpectedEOF
	}

	return nil
}

func (d *streamDecoder) Push(f Frame, emit func(Event) error) error {
	if !f.HasData || d.ended {
		return nil
	}
	var events []Event

	var err error

	switch d.wire {
	case OpenAIChat:
		events, err = d.chat(f)
	case AnthropicMessages:
		events, err = d.anthropic(f)
	case OpenAIResponses:
		events, err = d.responses(f)
	}

	if err != nil {
		return err
	}

	for _, ev := range events {
		if err := emit(ev); err != nil {
			return err
		}
	}

	return nil
}

func (d *streamDecoder) terminal(message string) Event {
	d.ended = true
	ev := d.meta
	ev.Kind = evEnd

	if message != "" {
		ev.Kind = evError
		ev.FinishReason = evError
		ev.Block.Text = message
	} else if ev.FinishReason == "" {
		ev.FinishReason = finishStop
		if d.sawTool {
			ev.FinishReason = finishTools
		}
	}

	return ev
}

func (d *streamDecoder) part(item, part int) int {
	it := d.items[item]
	if it == nil {
		it = &decodedItem{parts: make(map[int]int)}
		d.items[item] = it
	}

	if n, ok := it.parts[part]; ok {
		return n
	}

	n := d.next
	d.next++
	it.parts[part] = n

	return n
}

func (d *streamDecoder) block(index int, b Block) Event {
	b.Source = d.wire
	if b.Kind == blockCall {
		d.sawTool = true
	}

	if d.wire == OpenAIResponses && b.Kind == blockRefusal {
		d.meta.FinishReason = finishFilter
	}

	return Event{Kind: evBlock, Index: index, Block: b}
}

func (d *streamDecoder) metadata(id, model string, usage json.RawMessage) (Event, error) {
	if id != "" {
		d.meta.ID = id
	}

	if model != "" {
		d.meta.Model = model
	}

	if len(usage) > 0 && !bytes.Equal(usage, []byte("null")) {
		u, err := decodeUsage(d.wire, usage, d.meta.Usage)
		if err != nil {
			return Event{}, err
		}

		d.meta.Usage = u
	}

	return d.meta, nil
}

type chatDelta struct {
	Content, Reasoning, Refusal string
	ReasoningContent            string `json:"reasoning_content"`
	ToolCalls                   []struct {
		Index    int
		ID       string
		Function struct{ Name, Arguments string }
	} `json:"tool_calls"`
}

func (d *streamDecoder) chat(f Frame) ([]Event, error) {
	if bytes.Equal(f.Data, []byte(chatDone)) {
		return []Event{d.terminal("")}, nil
	}
	var chunk struct {
		ID, Model string
		Usage     json.RawMessage
		Error     *struct{ Message string }
		Choices   []struct {
			Index  int
			Delta  chatDelta
			Finish string `json:"finish_reason"`
		}
	}

	if err := json.Unmarshal(f.Data, &chunk); err != nil {
		return nil, err
	}

	if chunk.Error != nil {
		return []Event{d.failed(chunk.Error.Message)}, nil
	}

	m, err := d.metadata(chunk.ID, chunk.Model, chunk.Usage)
	if err != nil {
		return nil, err
	}

	out := []Event{m}

	for _, choice := range chunk.Choices {
		if choice.Index != 0 {
			return nil, fmt.Errorf("multiple streaming choices are unsupported")
		}

		delta := choice.Delta
		if delta.ReasoningContent != "" {
			delta.Reasoning = delta.ReasoningContent
		}

		for _, b := range []Block{{Kind: blockThink, Text: delta.Reasoning}, {Kind: blockText, Text: delta.Content}, {Kind: blockRefusal, Text: delta.Refusal}} {
			if b.Text != "" {
				if d.chatKind != b.Kind {
					d.chatKind, d.chatIndex = b.Kind, d.next
					d.next++
				}

				out = append(out, d.block(d.chatIndex, b))
			}
		}

		for _, call := range delta.ToolCalls {
			d.chatKind = ""
			b := Block{Kind: blockCall, ID: call.ID, Name: call.Function.Name, Data: json.RawMessage(call.Function.Arguments)}
			out = append(out, d.block(d.part(0, call.Index), b))
		}

		if choice.Finish != "" {
			d.meta.FinishReason = decodeFinish(choice.Finish)
			out = append(out, d.meta)
		}
	}

	return out, nil
}

func (d *streamDecoder) failed(msg string) Event {
	if msg == "" {
		msg = "upstream stream failed"
	}

	return d.terminal(msg)
}

type streamHeader struct {
	ID, Model         string
	Usage             json.RawMessage
	Error             struct{ Message string }
	IncompleteDetails struct{ Reason string } `json:"incomplete_details"`
}

func (d *streamDecoder) anthropic(f Frame) ([]Event, error) {
	var a struct {
		Type    string
		Index   int
		Message streamHeader
		Usage   json.RawMessage
		Error   struct{ Message string }
		Block   struct {
			Type, ID, Name, Text, Thinking, Signature, Data string
			Input                                           json.RawMessage
		} `json:"content_block"`
		Delta struct {
			Type, Text, Thinking, Signature string
			JSON                            string `json:"partial_json"`
			Finish                          string `json:"stop_reason"`
		}
	}

	if err := json.Unmarshal(f.Data, &a); err != nil {
		return nil, err
	}

	if a.Type == "" {
		a.Type = f.Name
	}
	var ev Event

	var err error

	switch a.Type {
	case "message_start":
		ev, err = d.metadata(a.Message.ID, a.Message.Model, a.Message.Usage)
	case "message_delta":
		d.meta.FinishReason = decodeFinish(a.Delta.Finish)
		ev, err = d.metadata("", "", a.Usage)
	case "message_stop":
		ev = d.terminal("")
	case evError:
		ev = d.failed(a.Error.Message)
	case "content_block_start":
		b := Block{Kind: a.Block.Type, Text: a.Block.Text, ID: a.Block.ID, Name: a.Block.Name, Signature: a.Block.Signature}
		switch b.Kind {
		case blockText:
		case blockThink:
			b.Text = a.Block.Thinking
		case blockRedacted:
			b.Signature = a.Block.Data
		case blockCall:
			if len(a.Block.Input) > 0 && !bytes.Equal(a.Block.Input, []byte("{}")) {
				b.Data = a.Block.Input
			}
		default:
			return nil, &UnsupportedError{Feature: b.Kind}
		}

		ev = d.block(a.Index, b)
	case "content_block_delta":
		var b Block

		switch a.Delta.Type {
		case "text_delta":
			b = Block{Kind: blockText, Text: a.Delta.Text}
		case "thinking_delta", "signature_delta":
			b = Block{Kind: blockThink, Text: a.Delta.Thinking, Signature: a.Delta.Signature}
		case "input_json_delta":
			b = Block{Kind: blockCall, Data: json.RawMessage(a.Delta.JSON)}
		default:
			return nil, nil
		}

		ev = d.block(a.Index, b)
	case "content_block_stop":
		ev = Event{Kind: evBlockEnd, Index: a.Index}
	default:
		return nil, nil
	}

	return []Event{ev}, err
}

type responseChunk struct {
	Type, Delta, Message, Arguments string
	Error                           struct{ Message string }
	Response                        streamHeader
	Item                            struct {
		Type, Name, Arguments string
		CallID                string `json:"call_id"`
	}
	Index   int `json:"output_index"`
	Part    int `json:"content_index"`
	Summary int `json:"summary_index"`
}

func (d *streamDecoder) responses(f Frame) ([]Event, error) {
	var r responseChunk

	if err := json.Unmarshal(f.Data, &r); err != nil {
		return nil, err
	}

	if r.Type == "" {
		r.Type = f.Name
	}

	switch r.Type {
	case "response.created":
		m, err := d.metadata(r.Response.ID, r.Response.Model, r.Response.Usage)
		return []Event{m}, err
	case "response.completed", "response.incomplete":
		_, err := d.metadata(r.Response.ID, r.Response.Model, r.Response.Usage)
		if err != nil {
			return nil, err
		}

		if r.Type == "response.incomplete" {
			reason := r.Response.IncompleteDetails.Reason
			if reason == "" {
				reason = statusIncomplete
			}

			d.meta.FinishReason = decodeFinish(reason)
		}

		return []Event{d.terminal("")}, nil
	case evError, "response.failed":
		msg := r.Response.Error.Message
		if msg == "" {
			msg = r.Error.Message
		}

		if msg == "" {
			msg = r.Message
		}

		return []Event{d.failed(msg)}, nil
	default:
		return d.responseBlocks(r), nil
	}
}

func (d *streamDecoder) responseBlocks(r responseChunk) []Event {
	it := d.items[r.Index]
	hasArgs := it != nil && it.hasArgs
	var b Block
	part := r.Part

	switch r.Type {
	case "response.output_item.added":
		if r.Item.Type != itemCall {
			return nil
		}

		b = Block{Kind: blockCall, Name: r.Item.Name, ID: r.Item.CallID}
		if r.Item.Arguments != "{}" {
			b.Data = json.RawMessage(r.Item.Arguments)
		}
	case "response.function_call_arguments.delta":
		b = Block{Kind: blockCall, Data: json.RawMessage(r.Delta)}
	case "response.function_call_arguments.done":
		if hasArgs || r.Arguments == "" {
			return nil
		}

		b = Block{Kind: blockCall, Data: json.RawMessage(r.Arguments)}
	case "response.output_item.done":
		var events []Event

		if r.Item.Type == itemCall && !hasArgs && (it == nil || r.Item.Arguments != "") {
			b = Block{Kind: blockCall, ID: r.Item.CallID, Name: r.Item.Name, Data: json.RawMessage(r.Item.Arguments)}
			events = append(events, d.block(d.part(r.Index, 0), b))
			it = d.items[r.Index]
		}

		if it != nil {
			for _, index := range slices.Sorted(maps.Values(it.parts)) {
				events = append(events, Event{Kind: evBlockEnd, Index: index})
			}

			delete(d.items, r.Index)
		}

		return events
	case "response.output_text.delta":
		b = Block{Kind: blockText, Text: r.Delta}
	case "response.refusal.delta":
		b = Block{Kind: blockRefusal, Text: r.Delta}
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		part = r.Summary
		b = Block{Kind: blockThink, Text: r.Delta}
	default:
		return nil
	}

	index := d.part(r.Index, part)

	if b.Kind == blockCall && len(b.Data) > 0 {
		d.items[r.Index].hasArgs = true
	}

	return []Event{d.block(index, b)}
}
