package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

const (
	blockText     = "text"
	blockImage    = "image"
	blockThink    = "thinking"
	blockRedacted = "redacted_thinking"
	blockCall     = "tool_use"
	blockResult   = "tool_result"
	blockRefusal  = "refusal"
	roleAssistant = "assistant"
	roleUser      = "user"
	roleTool      = "tool"
	roleSystem    = "system"
	itemCall      = "function_call"
	itemOutput    = "function_call_output"
	itemReason    = "reasoning"
	itemMessage   = "message"
	typeFunction  = "function"
)

type contentPart struct {
	Type, Text, Thinking, Signature, Data, Refusal, ID, Name string
	Input, Content                                           json.RawMessage
	UseID                                                    string          `json:"tool_use_id"`
	IsError                                                  bool            `json:"is_error"`
	ImageURL                                                 json.RawMessage `json:"image_url"`
	Source                                                   struct {
		Type, URL, Data string
		MediaType       string `json:"media_type"`
	}
}

func imageBlock(wire ID, p contentPart) (Block, error) {
	var link string

	switch wire {
	case AnthropicMessages:
		switch p.Source.Type {
		case "url":
			link = p.Source.URL
		case "base64":
			link = "data:" + p.Source.MediaType + ";base64," + p.Source.Data
		}
	case OpenAIChat:
		var im struct{ URL string }
		if err := json.Unmarshal(p.ImageURL, &im); err != nil {
			return Block{}, err
		}

		link = im.URL
	case OpenAIResponses:
		if err := json.Unmarshal(p.ImageURL, &link); err != nil {
			return Block{}, err
		}
	}

	if header, data, ok := strings.Cut(link, ","); ok && strings.HasPrefix(header, "data:image/") && strings.HasSuffix(header, ";base64") && data != "" {
		return Block{Kind: blockImage, Text: link}, nil
	}

	u, err := url.Parse(link)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return Block{}, errors.New("image must use http(s) or a base64 image data URL")
	}

	return Block{Kind: blockImage, Text: link}, nil
}

func toolArguments(raw json.RawMessage) json.RawMessage {
	if isNull(raw) {
		return json.RawMessage("{}")
	}

	return raw
}

func decodeParts(wire ID, raw json.RawMessage, nested bool) ([]Block, error) {
	if isNull(raw) {
		return nil, nil
	}

	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, err
		}

		return []Block{{Kind: blockText, Text: text}}, nil
	}

	var parts []contentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, err
	}
	var blocks []Block

	for _, p := range parts {
		b := Block{Kind: p.Type, Source: wire}

		switch p.Type {
		case blockText, "input_text", "output_text":
			b.Kind, b.Text = blockText, p.Text
		case blockImage, "image_url", "input_image":
			im, err := imageBlock(wire, p)
			if err != nil {
				return nil, err
			}

			b = im
		case blockRefusal:
			b.Text = p.Refusal
		case blockThink:
			b.Text, b.Signature = p.Thinking, p.Signature
		case blockRedacted:
			b.Signature = p.Data
		case blockCall:
			b.ID, b.Name, b.Data = p.ID, p.Name, toolArguments(p.Input)
		case blockResult:
			if nested {
				return nil, errors.New("nested tool_result")
			}

			inside, err := decodeParts(wire, p.Content, true)
			if err != nil {
				return nil, err
			}

			b.ID, b.IsError, b.Content = p.UseID, p.IsError, inside
		default:
			return nil, &UnsupportedError{p.Type}
		}

		blocks = append(blocks, b)
	}

	return blocks, nil
}

type messageItem struct {
	Type, Role, Name, Arguments, Reasoning, Refusal string
	Content, Output                                 json.RawMessage
	CallID                                          string `json:"call_id"`
	ToolID                                          string `json:"tool_call_id"`
	ReasonContent                                   string `json:"reasoning_content"`
	Encrypted                                       string `json:"encrypted_content"`
	Summary                                         []struct{ Text string }
	Calls                                           []struct {
		ID       string
		Function struct{ Name, Arguments string }
	} `json:"tool_calls"`
}

func decodeMessage(wire ID, raw json.RawMessage) (Message, error) {
	var it messageItem
	if err := json.Unmarshal(raw, &it); err != nil {
		return Message{}, err
	}

	if wire == OpenAIResponses && it.Type != "" && it.Type != itemMessage {
		switch it.Type {
		case itemCall:
			return Message{roleAssistant, []Block{{Kind: blockCall, ID: it.CallID, Name: it.Name, Data: toolArguments(json.RawMessage(it.Arguments))}}}, nil
		case itemOutput:
			content, err := decodeParts(wire, it.Output, true)
			return Message{roleUser, []Block{{Kind: blockResult, ID: it.CallID, Content: content}}}, err
		case itemReason:
			summary := make([]string, 0, len(it.Summary))
			for _, s := range it.Summary {
				summary = append(summary, s.Text)
			}

			return Message{roleAssistant, []Block{{Kind: blockThink, Text: strings.Join(summary, "\n"), Source: wire, Signature: it.Encrypted}}}, nil
		default:
			return Message{}, &UnsupportedError{it.Type}
		}
	}

	parts, err := decodeParts(wire, it.Content, false)
	if err != nil {
		return Message{}, err
	}

	switch it.Role {
	case roleTool:
		return Message{roleUser, []Block{{Kind: blockResult, ID: it.ToolID, Content: parts}}}, nil
	case "developer":
		it.Role = roleSystem
	case roleUser, roleAssistant, roleSystem:
	default:
		return Message{}, fmt.Errorf("invalid message role %q", it.Role)
	}

	if it.ReasonContent != "" {
		it.Reasoning = it.ReasonContent
	}

	if it.Reasoning != "" {
		parts = append([]Block{{Kind: blockThink, Text: it.Reasoning, Source: wire}}, parts...)
	}

	if it.Refusal != "" {
		parts = append(parts, Block{Kind: blockRefusal, Text: it.Refusal})
	}

	for _, call := range it.Calls {
		parts = append(parts, Block{Kind: blockCall, ID: call.ID, Name: call.Function.Name, Data: toolArguments(json.RawMessage(call.Function.Arguments))})
	}

	return Message{it.Role, parts}, nil
}

func decodeMessages(wire ID, items json.RawMessage) ([]Message, error) {
	var raws []json.RawMessage
	if err := json.Unmarshal(items, &raws); err != nil {
		return nil, err
	}
	var msgs []Message

	for _, raw := range raws {
		msg, err := decodeMessage(wire, raw)
		if err != nil {
			return nil, err
		}
		// Responses splits a single assistant turn into separate reasoning, message and call items.
		if wire == OpenAIResponses && msg.Role == roleAssistant && len(msgs) > 0 && msgs[len(msgs)-1].Role == roleAssistant {
			msgs[len(msgs)-1].Blocks = append(msgs[len(msgs)-1].Blocks, msg.Blocks...)
		} else {
			msgs = append(msgs, msg)
		}
	}

	return msgs, nil
}
