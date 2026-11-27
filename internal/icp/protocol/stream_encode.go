package protocol

import (
	"encoding/json"
	"fmt"
	"strings"
)

type streamBlock struct {
	kind, callID, name, signature string
	source                        ID
	position, toolIndex           int
	text                          strings.Builder // final Responses items and pending parallel Anthropic calls
	closed, hasArgs               bool
}

type streamEncoder struct {
	wire              ID
	opts              EncodeOpts
	id, model, finish string
	usage             *Usage
	seq, tools        int
	created           int64
	started           bool
	blocks            map[int]*streamBlock
	order             []*streamBlock
	anthActive        int
}

func (c wireCodec) NewStreamEncoder(opts EncodeOpts) StreamEncoder {
	return &streamEncoder{wire: ID(c), opts: opts, id: opts.ID, model: opts.Model, created: opts.Created, blocks: make(map[int]*streamBlock)}
}

// A semantic event can expand to several wire frames. Stop writing on the first client error.
type frameWriter struct {
	*streamEncoder
	emit func(Frame) error
	err  error
}

func (w *frameWriter) send(name string, body wireObject) {
	if w.err != nil {
		return
	}

	if name != "" {
		body["type"] = name
	}

	if w.wire == OpenAIResponses {
		body["sequence_number"] = w.seq
		w.seq++
	}

	data, err := json.Marshal(body)
	if err != nil {
		w.err = err
		return
	}

	var raw []byte
	if name != "" {
		raw = []byte("event: " + name + "\n")
	}

	raw = append(raw, "data: "...)
	raw = append(raw, data...)
	raw = append(raw, '\n', '\n')
	w.err = w.emit(Frame{Name: name, HasData: true, Data: data, Raw: raw})
}

func (e *streamEncoder) Push(ev Event, emit func(Frame) error) error {
	if !e.started && e.id == "" {
		e.id = ev.ID
	}

	if !e.started && e.model == "" {
		e.model = ev.Model
	}

	if ev.Usage != nil {
		e.usage = ev.Usage
	}

	if ev.FinishReason != "" {
		e.finish = ev.FinishReason
	}

	if ev.Kind == evError {
		return emit(Frame{Raw: ErrorFrame(e.wire, e.seq, ev.Block.Text)})
	}

	w := frameWriter{streamEncoder: e, emit: emit}

	if !e.started {
		e.started = true
		switch e.wire {
		case OpenAIChat:
			w.chat(wireObject{"role": roleAssistant}, nil)
		case AnthropicMessages:
			msg := wireObject{"type": itemMessage, "role": roleAssistant, "id": e.id, "model": e.model,
				"content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": e.anthUsage()}
			w.send("message_start", wireObject{itemMessage: msg})
		case OpenAIResponses:
			w.send("response.created", wireObject{"response": e.response("in_progress")})
		}
	}

	switch ev.Kind {
	case evBlock:
		w.chunk(ev)
	case evBlockEnd:
		if b := e.blocks[ev.Index]; b != nil && !b.closed {
			w.closeBlock(b)
		}
	case evEnd:
		w.finishStream()
	}

	return w.err
}

func (e *streamEncoder) anthUsage() wireObject {
	if e.usage == nil {
		return wireObject{"input_tokens": 0, "output_tokens": 0}
	}

	return renderUsage(AnthropicMessages, e.usage)
}

func (w *frameWriter) chat(delta wireObject, finish any) {
	w.send("", wireObject{"object": "chat.completion.chunk", "id": w.id, "created": w.created, "model": w.model,
		"choices": []any{wireObject{"index": 0, "delta": delta, "finish_reason": finish}}})
}

func (w *frameWriter) chunk(ev Event) {
	d := ev.Block
	if d.Kind == blockRedacted && w.wire != AnthropicMessages {
		return
	}

	b := w.blocks[ev.Index]

	first := b == nil
	if first {
		if w.wire == AnthropicMessages && w.anthActive < len(w.order) && w.order[w.anthActive].kind != blockCall {
			w.closeBlock(w.order[w.anthActive])
		}

		b = &streamBlock{kind: d.Kind, source: d.Source, callID: d.ID, name: d.Name, position: len(w.order), toolIndex: w.tools}

		if d.Kind == blockCall {
			w.tools++
		}

		w.blocks[ev.Index] = b
		w.order = append(w.order, b)
	}

	if b.closed {
		w.err = fmt.Errorf("delta after close for block %d", ev.Index)
		return
	}

	delta := d.Text
	if b.kind == blockCall {
		delta = string(d.Data)
		b.hasArgs = b.hasArgs || delta != ""
	}

	if d.Signature != "" {
		b.signature += d.Signature
	}

	switch w.wire {
	case OpenAIChat:
		w.chatDelta(b, first, delta)
	case OpenAIResponses:
		if first {
			w.responseOpen(b)
		}

		if delta != "" {
			w.responseDelta(b, delta)
			b.text.WriteString(delta)
		}
	case AnthropicMessages:
		// Chat can interleave parallel tool calls. Anthropic requires sequential blocks;
		// keep only the later calls pending while forwarding the first call incrementally.
		if w.anthActive == b.position {
			if first {
				w.anthOpen(b)
			}

			w.anthDelta(b, delta, d.Signature)
		} else {
			b.text.WriteString(delta)
		}
	}
}

func (w *frameWriter) chatDelta(b *streamBlock, first bool, delta string) {
	switch b.kind {
	case blockCall:
		fn := wireObject{"arguments": delta}

		call := wireObject{"index": b.toolIndex, typeFunction: fn}
		if first {
			call["type"] = typeFunction
			call["id"] = b.callID
			fn["name"] = b.name
		}

		w.chat(wireObject{finishTools: []any{call}}, nil)
	case blockThink:
		if delta != "" {
			w.chat(wireObject{"reasoning_content": delta}, nil)
		}
	case blockRefusal:
		w.chat(wireObject{blockRefusal: delta}, nil)
	case blockText:
		w.chat(wireObject{"content": delta}, nil)
	}
}

func (w *frameWriter) anthOpen(b *streamBlock) {
	var cb wireObject

	switch b.kind {
	case blockThink:
		cb = wireObject{"type": blockThink, blockThink: ""}
	case blockRedacted:
		cb = wireObject{"type": b.kind, "data": b.signature}
	case blockCall:
		cb = wireObject{"type": b.kind, "id": b.callID, "name": b.name, "input": wireObject{}}
	default:
		cb = wireObject{"type": blockText, blockText: ""}
	}

	w.send("content_block_start", wireObject{"index": b.position, "content_block": cb})
}
func (w *frameWriter) anthDelta(b *streamBlock, delta, signature string) {
	if delta != "" {
		kind, key := "text_delta", blockText
		switch b.kind {
		case blockThink:
			kind, key = "thinking_delta", blockThink
		case blockCall:
			kind, key = "input_json_delta", "partial_json"
		}

		w.send("content_block_delta", wireObject{"index": b.position, "delta": wireObject{"type": kind, key: delta}})
	}

	if b.kind == blockThink && b.source == AnthropicMessages && signature != "" {
		w.send("content_block_delta", wireObject{"index": b.position, "delta": wireObject{"type": "signature_delta", "signature": signature}})
	}
}

func (w *frameWriter) closeBlock(b *streamBlock) {
	b.closed = true

	if b.kind == blockCall && !b.hasArgs {
		switch w.wire {
		case OpenAIChat:
			w.chatDelta(b, false, "{}")
		case OpenAIResponses:
			b.text.WriteString("{}")
			w.responseDelta(b, "{}")
		}
	}

	switch w.wire {
	case AnthropicMessages:
		if b.position != w.anthActive {
			return
		}

		w.send("content_block_stop", wireObject{"index": b.position})

		w.anthActive++
		for w.anthActive < len(w.order) {
			next := w.order[w.anthActive]
			w.anthOpen(next)
			w.anthDelta(next, next.text.String(), next.signature)
			next.text.Reset()

			if !next.closed {
				break
			}

			w.send("content_block_stop", wireObject{"index": next.position})

			w.anthActive++
		}
	case OpenAIResponses:
		w.responseClose(b)
	}
}

func (w *frameWriter) finishStream() {
	if w.wire == AnthropicMessages && len(w.order) == 0 {
		w.chunk(Event{Block: Block{Kind: blockText}})
	}

	for _, b := range w.order {
		if !b.closed {
			w.closeBlock(b)
		}
	}

	if w.err != nil {
		return
	}

	reason := encodeFinish(w.wire, w.finish)

	switch w.wire {
	case AnthropicMessages:
		w.send("message_delta", wireObject{"delta": wireObject{"stop_reason": reason, "stop_sequence": nil}, "usage": w.anthUsage()})
		w.send("message_stop", wireObject{})
	case OpenAIResponses:
		w.send("response."+reason, wireObject{"response": w.response(reason)})
	case OpenAIChat:
		w.chat(wireObject{}, reason)

		if w.opts.IncludeUsage && w.usage != nil {
			w.send("", wireObject{"object": "chat.completion.chunk", "id": w.id, "created": w.created, "model": w.model,
				"choices": []any{}, "usage": renderUsage(w.wire, w.usage)})
		}

		if w.err == nil {
			w.err = w.emit(Frame{HasData: true, Data: []byte(chatDone), Raw: []byte("data: [DONE]\n\n")})
		}
	}
}
