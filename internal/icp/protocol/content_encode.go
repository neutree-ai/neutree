package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

type wireObject = map[string]any

func mediaPart(wire ID, b Block, role string) wireObject {
	if b.Kind == blockImage {
		switch wire {
		case AnthropicMessages:
			source := wireObject{"type": "url", "url": b.Text}

			if rest, ok := strings.CutPrefix(b.Text, "data:"); ok {
				media, data, _ := strings.Cut(rest, ";base64,")
				source = wireObject{"type": "base64", "media_type": media, "data": data}
			}

			return wireObject{"type": blockImage, "source": source}
		case OpenAIResponses:
			return wireObject{"type": "input_image", "image_url": b.Text}
		default:
			return wireObject{"type": "image_url", "image_url": wireObject{"url": b.Text}}
		}
	}

	kind := blockText
	if wire == OpenAIResponses {
		kind = "input_text"

		if b.Kind == blockRefusal {
			return wireObject{"type": blockRefusal, "refusal": b.Text}
		}

		if role == roleAssistant {
			return wireObject{"type": "output_text", "text": b.Text, "annotations": []any{}}
		}
	}

	return wireObject{"type": kind, "text": b.Text}
}

func toolOutput(wire ID, b Block) any {
	parts := b.Content
	// Chat and Responses have no is_error field. Keep failure visible in their tool output.
	if b.IsError && wire != AnthropicMessages {
		parts = append([]Block{{Kind: blockText, Text: "Tool error: "}}, parts...)
	}
	var text strings.Builder
	var out []wireObject

	for _, p := range parts {
		if p.Kind == blockImage && wire == OpenAIChat {
			// The following user message carries these images on Chat.
			continue
		}

		out = append(out, mediaPart(wire, p, roleUser))

		if p.Kind != blockImage {
			text.WriteString(p.Text)
		}
	}

	for _, p := range parts {
		if p.Kind == blockImage && wire != OpenAIChat {
			return out
		}
	}

	return text.String()
}

func anthropicHistoryBlock(b Block, history bool) (wireObject, error) {
	switch b.Kind {
	case blockThink:
		if history && (b.Source != AnthropicMessages || b.Signature == "") {
			return nil, nil
		}

		o := wireObject{"type": blockThink, "thinking": b.Text}
		if b.Source == AnthropicMessages && b.Signature != "" {
			o["signature"] = b.Signature
		}

		return o, nil
	case blockRedacted:
		if b.Source != AnthropicMessages || b.Signature == "" {
			return nil, nil
		}

		return wireObject{"type": blockRedacted, "data": b.Signature}, nil
	case blockCall:
		data := bytes.TrimSpace(b.Data)
		var input any = b.Data

		if len(data) == 0 && !history {
			input = wireObject{}
		} else if !json.Valid(data) || data[0] != '{' {
			if history {
				return nil, errors.New("tool_use input must be a JSON object")
			}

			input = wireObject{"raw": string(b.Data)}
		}

		return wireObject{"type": blockCall, "id": b.ID, "name": b.Name, "input": input}, nil
	case blockResult:
		o := wireObject{"type": blockResult, "tool_use_id": b.ID, "content": toolOutput(AnthropicMessages, b)}
		if b.IsError {
			o["is_error"] = true
		}

		return o, nil
	default:
		return mediaPart(AnthropicMessages, b, roleUser), nil
	}
}

func renderChatMessage(m Message, toolImages *[]wireObject) []wireObject {
	var out, parts, calls []wireObject
	var thinking, refusal strings.Builder
	flush := func() {
		if len(parts)+len(calls)+thinking.Len()+refusal.Len() == 0 {
			return
		}

		if len(*toolImages) > 0 {
			if m.Role == roleUser {
				parts = append(*toolImages, parts...)
			} else {
				out = append(out, wireObject{"role": roleUser, "content": *toolImages})
			}

			*toolImages = nil
		}

		o := wireObject{"role": m.Role, "content": nil}
		if len(parts) == 1 && parts[0]["type"] == blockText {
			o["content"] = parts[0]["text"]
		} else if len(parts) > 0 {
			o["content"] = parts
		}

		if len(calls) > 0 {
			o["tool_calls"] = calls
		}

		if thinking.Len() > 0 {
			o["reasoning_content"] = thinking.String()
		}

		if refusal.Len() > 0 {
			o["refusal"] = refusal.String()
		}

		out = append(out, o)
		parts, calls = nil, nil

		thinking.Reset()
		refusal.Reset()
	}

	for _, b := range m.Blocks {
		switch b.Kind {
		case blockResult:
			flush()

			out = append(out, wireObject{"role": roleTool, "tool_call_id": b.ID, "content": toolOutput(OpenAIChat, b)})

			for _, p := range b.Content {
				if p.Kind == blockImage {
					*toolImages = append(*toolImages, mediaPart(OpenAIChat, p, roleUser))
				}
			}
		case blockCall:
			calls = append(calls, wireObject{"type": typeFunction, "id": b.ID, "function": wireObject{"name": b.Name, "arguments": string(b.Data)}})
		case blockThink:
			thinking.WriteString(b.Text)
		case blockRedacted: // encrypted Anthropic data is meaningful only to Anthropic
		case blockRefusal:
			refusal.WriteString(b.Text)
		default:
			parts = append(parts, mediaPart(OpenAIChat, b, m.Role))
		}
	}

	flush()

	return out
}

func renderResponseItems(m Message) []wireObject {
	var out, parts []wireObject
	flush := func() {
		if len(parts) > 0 {
			out = append(out, wireObject{"type": itemMessage, "role": m.Role, "content": parts})
			parts = nil
		}
	}

	for _, b := range m.Blocks {
		var item wireObject

		switch b.Kind {
		case blockCall:
			item = wireObject{"type": itemCall, "name": b.Name, "arguments": string(b.Data), "call_id": b.ID}
		case blockResult:
			item = wireObject{"type": itemOutput, "call_id": b.ID, "output": toolOutput(OpenAIResponses, b)}
		case blockThink:
			summary := []wireObject{}
			if b.Text != "" {
				summary = append(summary, wireObject{"type": "summary_text", "text": b.Text})
			}

			item = wireObject{"type": itemReason, "summary": summary}
			if b.Source == OpenAIResponses && b.Signature != "" {
				item["encrypted_content"] = b.Signature
			}
		case blockRedacted:
			continue
		default:
			parts = append(parts, mediaPart(OpenAIResponses, b, m.Role))
			continue
		}

		flush()

		out = append(out, item)
	}

	flush()

	return out
}

func renderMessages(wire ID, msgs []Message, history bool) ([]wireObject, error) {
	out := []wireObject{}
	var content []wireObject
	var prevRole string
	var toolImages []wireObject
	flush := func() {
		if len(content) > 0 {
			out = append(out, wireObject{"role": prevRole, "content": content})
			content = nil
		}
	}

	for _, m := range msgs {
		switch wire {
		case OpenAIChat:
			out = append(out, renderChatMessage(m, &toolImages)...)
		case OpenAIResponses:
			out = append(out, renderResponseItems(m)...)
		case AnthropicMessages:
			var parts []wireObject

			for _, b := range m.Blocks {
				p, err := anthropicHistoryBlock(b, history)
				if err != nil {
					return nil, err
				}

				if p != nil {
					parts = append(parts, p)
				}
			}

			if len(parts) == 0 {
				continue
			}

			if m.Role != prevRole {
				flush()

				prevRole = m.Role
			}

			content = append(content, parts...)
		}
	}

	flush()

	if len(toolImages) > 0 {
		out = append(out, wireObject{"role": roleUser, "content": toolImages})
	}

	return out, nil
}
