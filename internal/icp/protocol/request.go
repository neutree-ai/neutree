package protocol

import (
	"encoding/json"
	"errors"
	"strings"
)

const (
	responsesMaxTokens  = "max_output_tokens"
	maxTokens           = "max_tokens"
	completionMaxTokens = "max_completion_tokens"
	stopSequences       = "stop_sequences"
	paramStop           = "stop"
	choiceRequired      = "required"
)

type UnsupportedError struct{ Feature string }

func (e *UnsupportedError) Error() string {
	return "unsupported stateless gateway feature: " + e.Feature
}

var nullableReferences = [...]string{"previous_response_id", "conversation"}

func validateStateless(e Envelope) error {
	if _, ok := e.Fields["item_reference"]; ok {
		return &UnsupportedError{"item_reference"}
	}

	for _, k := range nullableReferences {
		if !isNull(e.Fields[k]) {
			return &UnsupportedError{k}
		}
	}

	for _, k := range []string{"store", "background"} {
		if raw := e.Fields[k]; !isNull(raw) {
			var b bool
			if err := json.Unmarshal(raw, &b); err != nil {
				return err
			}

			if b {
				return &UnsupportedError{k}
			}
		}
	}

	return nil
}

func parseTools(wire ID, raw json.RawMessage) ([]Tool, error) {
	if isNull(raw) {
		return nil, nil
	}
	var defs []struct {
		Tool
		Type        string
		Function    *Tool
		InputSchema json.RawMessage `json:"input_schema"`
	}

	if err := json.Unmarshal(raw, &defs); err != nil {
		return nil, err
	}

	tools := make([]Tool, 0, len(defs))

	for _, d := range defs {
		t := d.Tool

		switch {
		case wire == AnthropicMessages && (d.Type == "" || d.Type == "custom"):
			t.Parameters = d.InputSchema
		case wire == OpenAIResponses && d.Type == typeFunction:
		case wire == OpenAIChat && d.Type == typeFunction && d.Function != nil:
			t = *d.Function
		default:
			return nil, &UnsupportedError{"tool type " + d.Type}
		}

		if t.Name == "" {
			return nil, errors.New("tool name is required")
		}

		tools = append(tools, t)
	}

	return tools, nil
}

// Same-wire forwarding preserves native content blocks. Inspect only the fields
// which affect the gateway's stateless contract and the supported image sources.
func ValidateRequest(wire ID, e Envelope) error {
	if err := validateStateless(e); err != nil {
		return err
	}

	if _, err := parseTools(wire, e.Fields["tools"]); err != nil {
		return err
	}
	var items []struct {
		Type            string
		Content, Output json.RawMessage
	}

	key := "messages"
	if wire == OpenAIResponses {
		key = "input"
	}

	raw := e.Fields[key]
	if len(raw) == 0 || raw[0] != '[' {
		return nil
	}

	if err := json.Unmarshal(raw, &items); err != nil {
		return err
	}

	for _, it := range items {
		if it.Type == "item_reference" {
			return &UnsupportedError{it.Type}
		}

		for _, v := range []json.RawMessage{it.Content, it.Output} {
			if err := validateImageParts(wire, v, false); err != nil {
				return err
			}
		}
	}

	return nil
}

func validateImageParts(wire ID, v json.RawMessage, nested bool) error {
	if len(v) == 0 || v[0] != '[' {
		return nil
	}

	var parts []contentPart
	if err := json.Unmarshal(v, &parts); err != nil {
		return err
	}

	for _, p := range parts {
		switch p.Type {
		case blockImage, "image_url", "input_image":
			if _, err := imageBlock(wire, p); err != nil {
				return err
			}
		case blockResult:
			if !nested {
				if err := validateImageParts(wire, p.Content, true); err != nil {
					return err
				}
			}
		case "item_reference":
			return &UnsupportedError{p.Type}
		}
	}

	return nil
}

// Cross-protocol conversion implements only the default plain-text output.
func validateOutputFormat(wire ID, fields map[string]json.RawMessage) error {
	key := "response_format"
	format := fields[key]

	if wire == OpenAIResponses {
		text := fields["text"]
		if isNull(text) {
			return nil
		}

		key = "text.format"
		var options map[string]json.RawMessage

		if err := json.Unmarshal(text, &options); err != nil {
			return &UnsupportedError{key}
		}

		for name, value := range options {
			if name != "format" && !isNull(value) {
				return &UnsupportedError{key}
			}
		}

		format = options["format"]
	} else if wire != OpenAIChat {
		return nil
	}

	if !isNull(format) {
		var spec map[string]string
		if err := json.Unmarshal(format, &spec); err != nil || len(spec) != 1 || spec["type"] != blockText {
			return &UnsupportedError{key}
		}
	}

	return nil
}

func (w wireCodec) DecodeRequest(e Envelope) (Request, error) {
	wire := ID(w)

	r := Request{Model: e.Model, Stream: e.Stream, Params: make(map[string]json.RawMessage)}
	if err := validateStateless(e); err != nil {
		return r, err
	}

	if err := validateOutputFormat(wire, e.Fields); err != nil {
		return r, err
	}

	if n := e.Fields["n"]; wire == OpenAIChat && !isNull(n) {
		var choices int
		if err := json.Unmarshal(n, &choices); err != nil {
			return r, err
		}

		if choices > 1 {
			return r, &UnsupportedError{"multiple choices"}
		}
	}

	tools, err := parseTools(wire, e.Fields["tools"])
	if err != nil {
		return r, err
	}

	r.Tools = tools
	if err = r.readChoice(e.Fields); err != nil {
		return r, err
	}

	for _, k := range []string{"temperature", "top_p", maxTokens, completionMaxTokens, responsesMaxTokens, paramStop, stopSequences} {
		v := e.Fields[k]
		if isNull(v) {
			continue
		}

		switch k {
		case responsesMaxTokens, completionMaxTokens:
			k = maxTokens
		case stopSequences:
			k = paramStop
		}

		r.Params[k] = v
	}

	system, items := e.Fields["system"], e.Fields["messages"]
	if wire == OpenAIResponses {
		system, items = e.Fields["instructions"], e.Fields["input"]
	}

	if !isNull(system) {
		parts, err := decodeParts(wire, system, false)
		if err != nil {
			return r, err
		}

		r.Messages = append(r.Messages, Message{roleSystem, parts})
	}

	if len(items) > 0 && items[0] == '"' && wire == OpenAIResponses {
		parts, err := decodeParts(wire, items, false)
		r.Messages = append(r.Messages, Message{roleUser, parts})

		return r, err
	}

	msgs, err := decodeMessages(wire, items)
	r.Messages = append(r.Messages, msgs...)

	return r, err
}

func (r *Request) readChoice(fields map[string]json.RawMessage) error {
	if raw := fields["parallel_tool_calls"]; !isNull(raw) {
		if err := json.Unmarshal(raw, &r.Parallel); err != nil {
			return err
		}
	}

	raw := fields["tool_choice"]
	if isNull(raw) {
		return nil
	}

	if raw[0] == '"' {
		if err := json.Unmarshal(raw, &r.ToolChoice); err != nil {
			return err
		}
	} else {
		var c struct {
			Type, Name string
			Function   struct{ Name string }
			Disable    *bool `json:"disable_parallel_tool_use"`
		}

		if err := json.Unmarshal(raw, &c); err != nil {
			return err
		}

		r.ToolChoice, r.ToolName = c.Type, c.Name
		if c.Type == typeFunction && c.Function.Name != "" {
			r.ToolName = c.Function.Name
		}

		if c.Disable != nil {
			allow := !*c.Disable
			r.Parallel = &allow
		}
	}

	switch r.ToolChoice {
	case "any":
		r.ToolChoice = choiceRequired
	case roleTool, typeFunction:
		r.ToolChoice = typeFunction
		if r.ToolName == "" {
			return errors.New("tool_choice requires a name")
		}
	case "auto", "none", choiceRequired:
	default:
		return &UnsupportedError{"tool_choice " + r.ToolChoice}
	}

	return nil
}

func (r Request) writeTools(wire ID, out wireObject) {
	if len(r.Tools) > 0 {
		defs := make([]wireObject, 0, len(r.Tools))

		for _, t := range r.Tools {
			obj := wireObject{"name": t.Name}
			if t.Description != "" {
				obj["description"] = t.Description
			}

			schema := t.Parameters
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}

			if wire == AnthropicMessages {
				obj["input_schema"] = schema
			} else {
				obj["parameters"] = schema
			}

			if t.Strict != nil {
				obj["strict"] = *t.Strict
			}

			if wire == OpenAIResponses {
				obj["type"] = typeFunction
				obj["strict"] = t.Strict != nil && *t.Strict
			}

			if wire == OpenAIChat {
				obj = wireObject{"type": typeFunction, "function": obj}
			}

			defs = append(defs, obj)
		}

		out["tools"] = defs
	}

	mode := r.ToolChoice
	if wire == AnthropicMessages {
		if mode == "" && r.Parallel == nil {
			return
		}

		if mode == "" {
			mode = "auto"
		}

		if mode == choiceRequired {
			mode = "any"
		}

		choice := wireObject{"type": mode}
		if mode == typeFunction {
			choice["type"], choice["name"] = roleTool, r.ToolName
		}

		if r.Parallel != nil && mode != "none" {
			choice["disable_parallel_tool_use"] = !*r.Parallel
		}

		out["tool_choice"] = choice

		return
	}

	if r.Parallel != nil {
		out["parallel_tool_calls"] = *r.Parallel
	}

	if mode == typeFunction {
		choice := wireObject{"type": mode}
		if wire == OpenAIChat {
			choice["function"] = wireObject{"name": r.ToolName}
		} else {
			choice["name"] = r.ToolName
		}

		out["tool_choice"] = choice
	} else if mode != "" {
		out["tool_choice"] = mode
	}
}

func (w wireCodec) EncodeRequest(r Request, opts EncodeOpts) ([]byte, error) {
	wire := ID(w)

	model := r.Model
	if opts.Model != "" {
		model = opts.Model
	}

	out := wireObject{"model": model, "stream": r.Stream}
	msgs := r.Messages

	if wire != OpenAIChat {
		var instruction []string
		msgs = make([]Message, 0, len(r.Messages))

		for _, m := range r.Messages {
			if m.Role == roleSystem {
				for _, b := range m.Blocks {
					if b.Kind != blockText {
						return nil, &UnsupportedError{"non-text system message"}
					}

					instruction = append(instruction, b.Text)
				}
			} else {
				msgs = append(msgs, m)
			}
		}

		if len(instruction) > 0 {
			key := "system"
			if wire == OpenAIResponses {
				key = "instructions"
			}

			out[key] = strings.Join(instruction, "\n\n")
		}
	}

	body, err := renderMessages(wire, msgs, true)
	if err != nil {
		return nil, err
	}

	if wire == OpenAIResponses {
		out["input"] = body
		out["store"] = false
	} else {
		out["messages"] = body
	}

	r.writeTools(wire, out)

	if wire == OpenAIChat && r.Stream && opts.IncludeUsage {
		out["stream_options"] = wireObject{"include_usage": true}
	}

	if err := writeParams(wire, out, r.Params, opts.Defaults); err != nil {
		return nil, err
	}

	return json.Marshal(out)
}

func writeParams(wire ID, out wireObject, params, defaults map[string]json.RawMessage) error {
	for _, set := range []map[string]json.RawMessage{params, defaults} {
		for k, v := range set {
			if isNull(v) {
				continue
			}

			k, v, err := wireParam(wire, k, v)
			if err != nil {
				return err
			}

			if _, exists := out[k]; exists {
				continue
			}

			out[k] = v
		}
	}

	if wire == AnthropicMessages && out[maxTokens] == nil {
		out[maxTokens] = 4096
	}

	return nil
}

func wireParam(wire ID, k string, v json.RawMessage) (string, json.RawMessage, error) {
	switch k {
	case maxTokens, completionMaxTokens, responsesMaxTokens:
		k = maxTokens
		if wire == OpenAIResponses {
			k = responsesMaxTokens
		}
	case paramStop, stopSequences:
		if wire == OpenAIResponses {
			return "", nil, &UnsupportedError{"stop sequences on Responses"}
		}

		k = paramStop
		if wire == AnthropicMessages {
			k = stopSequences
		}
	}

	if k == stopSequences && len(v) > 0 && v[0] == '"' {
		// The value is already a JSON string; Anthropic expects an array of strings.
		v = append(append(json.RawMessage{'['}, v...), ']')
	}

	return k, v, nil
}
