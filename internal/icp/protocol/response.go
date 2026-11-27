package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
)

const (
	finishStop       = "stop"
	finishLength     = "length"
	finishTools      = "tool_calls"
	finishFilter     = "content_filter"
	statusComplete   = "completed"
	statusIncomplete = "incomplete"
)

func decodeFinish(reason string) string {
	switch reason {
	case "end_turn", "stop_sequence", statusComplete:
		return finishStop
	case "max_tokens", "max_output_tokens", statusIncomplete:
		return finishLength
	case blockCall:
		return finishTools
	case blockRefusal:
		return finishFilter
	default:
		return reason
	}
}

func encodeFinish(wire ID, reason string) string {
	switch wire {
	case AnthropicMessages:
		switch reason {
		case finishTools:
			return blockCall
		case finishLength:
			return "max_tokens"
		case finishFilter:
			return blockRefusal
		case "", finishStop:
			return "end_turn"
		}
	case OpenAIResponses:
		if reason == finishLength || reason == finishFilter {
			return statusIncomplete
		}

		return statusComplete
	}

	if reason == "" {
		return finishStop
	}

	return reason
}

func responsesIncompleteReason(reason string) string {
	if reason == finishFilter {
		return finishFilter
	}

	return "max_output_tokens"
}

type responseHeader struct {
	ID, Model, Status string
	StopReason        string                  `json:"stop_reason"`
	Incomplete        struct{ Reason string } `json:"incomplete_details"`
	Usage             json.RawMessage
	Error             *struct{ Message string }
}

func (h *responseHeader) metadata(wire ID) (Response, error) {
	if h.Error != nil {
		return Response{}, fmt.Errorf("upstream error: %s", h.Error.Message)
	}

	reason := h.StopReason

	if wire == OpenAIResponses {
		reason = h.Status
		if reason == statusIncomplete && h.Incomplete.Reason != "" {
			reason = h.Incomplete.Reason
		}
	}

	u, err := decodeUsage(wire, h.Usage, nil)

	return Response{ID: h.ID, Model: h.Model, FinishReason: decodeFinish(reason), Usage: u}, err
}

// ObserveResponse reads native Anthropic or Responses metadata without decoding
// vendor-specific content blocks. The proxy relays the original bytes.
func ObserveResponse(wire ID, raw []byte) (Response, error) {
	var h responseHeader
	if err := json.Unmarshal(raw, &h); err != nil {
		return Response{}, err
	}

	return h.metadata(wire)
}

func (c wireCodec) DecodeResponse(raw []byte) (Response, error) {
	var v struct {
		responseHeader
		Choices []struct {
			Message json.RawMessage
			Reason  string `json:"finish_reason"`
		}
		Content, Output json.RawMessage
	}

	if err := json.Unmarshal(raw, &v); err != nil {
		return Response{}, err
	}

	wire := ID(c)
	r, err := v.metadata(wire)

	if err != nil {
		return r, err
	}

	switch wire {
	case OpenAIChat:
		if len(v.Choices) != 1 {
			return r, errors.New("expected one Chat response choice")
		}
		var m Message
		m, err = decodeMessage(wire, v.Choices[0].Message)
		r.Blocks = m.Blocks
		r.FinishReason = decodeFinish(v.Choices[0].Reason)
	case AnthropicMessages:
		r.Blocks, err = decodeParts(wire, v.Content, false)
	case OpenAIResponses:
		var msgs []Message
		msgs, err = decodeMessages(wire, v.Output)

		for _, m := range msgs {
			r.Blocks = append(r.Blocks, m.Blocks...)
		}

		if r.FinishReason == finishStop {
			for _, b := range r.Blocks {
				switch b.Kind {
				case blockCall:
					r.FinishReason = finishTools
				case blockRefusal:
					r.FinishReason = finishFilter
				}
			}
		}
	}

	return r, err
}

func itemID(kind, id string, index int) string {
	prefix := "msg"

	switch kind {
	case itemCall, blockCall:
		prefix = "fc"
	case itemReason, blockThink:
		prefix = "rs"
	}

	return fmt.Sprintf("%s_%s_%d", prefix, id, index)
}

func (c wireCodec) EncodeResponse(r Response, opts EncodeOpts) ([]byte, error) {
	wire := ID(c)

	if opts.ID != "" {
		r.ID = opts.ID
	}

	if opts.Model != "" {
		r.Model = opts.Model
	}

	msgs, err := renderMessages(wire, []Message{{Role: roleAssistant, Blocks: r.Blocks}}, false)
	if err != nil {
		return nil, err
	}

	reason := encodeFinish(wire, r.FinishReason)
	usage := renderUsage(wire, r.Usage)
	var out map[string]any

	switch wire {
	case OpenAIResponses:
		for i, item := range msgs {
			kind, _ := item["type"].(string)
			item["id"] = itemID(kind, r.ID, i)
			item["status"] = reason
		}

		out = map[string]any{"id": r.ID, "model": r.Model, "object": "response", "created_at": opts.Created, "output": msgs,
			"status": reason, "error": nil, "incomplete_details": nil, "usage": usage, "store": false}
		if reason == statusIncomplete {
			out["incomplete_details"] = map[string]string{"reason": responsesIncompleteReason(r.FinishReason)}
		}
	default:
		msg := map[string]any{"role": roleAssistant}
		if len(msgs) > 0 {
			msg = msgs[0]
		}

		if wire == AnthropicMessages {
			if msg["content"] == nil {
				msg["content"] = []wireObject{{"type": blockText, "text": ""}}
			}

			if usage == nil {
				usage = renderUsage(wire, &Usage{})
			}

			out = msg
			out["id"], out["model"], out["type"] = r.ID, r.Model, itemMessage
			out["stop_reason"], out["stop_sequence"], out["usage"] = reason, nil, usage
		} else {
			out = map[string]any{"id": r.ID, "model": r.Model, "object": "chat.completion", "created": opts.Created,
				"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": reason}}}
			if usage != nil {
				out["usage"] = usage
			}
		}
	}

	return json.Marshal(out)
}
