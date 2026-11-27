package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestRequestConversions(t *testing.T) {
	samples := map[ID]string{
		OpenAIChat:        `{"model":"m","stream":true,"temperature":0,"tool_choice":{"type":"function","function":{"name":"f"}},"parallel_tool_calls":false,"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}],"messages":[{"role":"developer","content":"rules"},{"role":"assistant","reasoning":"thought","tool_calls":[{"id":"c1","function":{"name":"f","arguments":""}},{"id":"c2","function":{"name":"f","arguments":"null"}}]},{"role":"tool","tool_call_id":"c1","content":"one"},{"role":"tool","tool_call_id":"c2","content":"two"},{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://images.example/i.png"}},{"type":"text","text":"next"}]}]}`,
		AnthropicMessages: `{"model":"m","stream":true,"temperature":0,"tool_choice":{"type":"tool","name":"f","disable_parallel_tool_use":true},"tools":[{"name":"f","input_schema":{"type":"object"}}],"system":[{"type":"text","text":"rules"}],"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"thought","signature":"signed"},{"type":"tool_use","id":"c1","name":"f","input":null},{"type":"tool_use","id":"c2","name":"f"}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":"one","is_error":true},{"type":"tool_result","tool_use_id":"c2","content":"two"},{"type":"image","source":{"type":"url","url":"https://images.example/i.png"}},{"type":"text","text":"next"}]}]}`,
		OpenAIResponses:   `{"model":"m","stream":true,"temperature":0,"tool_choice":{"type":"function","name":"f"},"tools":[{"type":"function","name":"f","parameters":{"type":"object"}}],"parallel_tool_calls":false,"instructions":"rules","input":[{"type":"reasoning","summary":[{"text":"thought"}],"encrypted_content":"cipher"},{"type":"function_call","id":"fc1","call_id":"c1","name":"f","arguments":"null"},{"type":"function_call","id":"fc2","call_id":"c2","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"one"},{"type":"function_call_output","call_id":"c2","output":"two"},{"role":"user","content":[{"type":"input_image","image_url":"https://images.example/i.png"},{"type":"input_text","text":"next"}]}]}`,
	}
	for from, raw := range samples {
		env, err := Inspect([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		req, err := wireCodec(from).DecodeRequest(env)
		if err != nil {
			t.Fatal(from, err)
		}
		for to := range samples {
			if from == to {
				continue
			}
			t.Run(string(from)+"_to_"+string(to), func(t *testing.T) {
				b, err := wireCodec(to).EncodeRequest(req, EncodeOpts{IncludeUsage: true, Defaults: map[string]json.RawMessage{"temperature": []byte("0.7")}})
				if err != nil {
					t.Fatal(err)
				}
				wire := string(b)
				for _, want := range []string{"rules", "https://images.example/i.png", "next", `"temperature":0`} {
					if !strings.Contains(wire, want) {
						t.Fatal(wire)
					}
				}
				for _, id := range []string{"c1", "c2"} {
					if strings.Count(wire, `"`+id+`"`) != 2 {
						t.Fatal(wire)
					}
				}
				emptyArgs := `"arguments":"{}"`
				if to == AnthropicMessages {
					emptyArgs = `"input":{}`
				}
				if strings.Count(wire, emptyArgs) != 2 {
					t.Fatalf("empty arguments: %s", wire)
				}
				if to == AnthropicMessages {
					if strings.Contains(wire, "thinking") || !strings.Contains(wire, `"disable_parallel_tool_use":true`) || !strings.Contains(wire, `"max_tokens":4096`) {
						t.Fatal(wire)
					}
				} else if !strings.Contains(wire, "thought") || !strings.Contains(wire, `"parallel_tool_calls":false`) {
					t.Fatal(wire)
				}
				if to == OpenAIResponses && (!strings.Contains(wire, `"strict":false`) || !strings.Contains(wire, `"store":false`)) {
					t.Fatal(wire)
				}
				if from == AnthropicMessages && !strings.Contains(wire, "Tool error: one") {
					t.Fatal(wire)
				}
				if to == OpenAIChat {
					var chat struct {
						Messages []struct {
							Role    string
							Content json.RawMessage
						}
					}
					if err := json.Unmarshal(b, &chat); err != nil {
						t.Fatal(err)
					}
					n := len(chat.Messages)
					if n < 3 || chat.Messages[n-3].Role != "tool" || chat.Messages[n-2].Role != "tool" || chat.Messages[n-1].Role != "user" || !strings.Contains(wire, `"include_usage":true`) {
						t.Fatal(wire)
					}
				}
			})
		}
	}
}

func TestSignedThinkingHistory(t *testing.T) {
	req := Request{Messages: []Message{
		{Role: "user", Blocks: []Block{{Kind: "text", Text: "first"}}},
		{Role: "assistant", Blocks: []Block{{Kind: "thinking", Source: OpenAIResponses, Signature: "foreign"}}},
		{Role: "user", Blocks: []Block{{Kind: "text", Text: "second"}}},
		{Role: "assistant", Blocks: []Block{{Kind: "thinking", Source: AnthropicMessages, Text: "unsigned"},
			{Kind: "thinking", Source: AnthropicMessages, Text: "trusted", Signature: "signature"},
			{Kind: "redacted_thinking", Source: AnthropicMessages, Signature: "encrypted"}, {Kind: "text", Text: "answer"}}},
	}}
	b, err := wireCodec(AnthropicMessages).EncodeRequest(req, EncodeOpts{})
	if err != nil || bytes.Contains(b, []byte("foreign")) || bytes.Contains(b, []byte("unsigned")) || !bytes.Contains(b, []byte("trusted")) || !bytes.Contains(b, []byte("encrypted")) || !bytes.Contains(b, []byte("answer")) {
		t.Fatal(string(b), err)
	}
	var got struct {
		Messages []struct {
			Role    string
			Content []struct{ Text string }
		}
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 2 || got.Messages[0].Role != "user" || got.Messages[1].Role != "assistant" {
		t.Fatal(string(b))
	}
	parts := got.Messages[0].Content
	if len(parts) != 2 || parts[0].Text != "first" || parts[1].Text != "second" {
		t.Fatal(parts)
	}
}

func TestUnsupportedRequestFeatures(t *testing.T) {
	for _, tc := range []struct {
		wire    ID
		feature string
	}{
		{OpenAIResponses, `"previous_response_id":""`}, {OpenAIResponses, `"conversation":""`},
		{OpenAIResponses, `"previous_response_id":"resp"`}, {OpenAIResponses, `"input":[{"type":"item_reference","id":"item"}]`},
		{OpenAIChat, `"store":true`}, {AnthropicMessages, `"background":true`}, {OpenAIResponses, `"conversation":"id"`},
		{OpenAIResponses, `"tools":[{"type":"web_search_preview"}]`}, {AnthropicMessages, `"tools":[{"type":"bash_20250124"}]`},
	} {
		e, err := Inspect([]byte("{" + tc.feature + "}"))
		if err != nil {
			t.Fatal(err)
		}
		_, crossErr := wireCodec(tc.wire).DecodeRequest(e)
		var unsupported *UnsupportedError
		if !errors.As(crossErr, &unsupported) || !errors.As(ValidateRequest(tc.wire, e), &unsupported) {
			t.Fatal(tc, crossErr)
		}
	}
	for _, tc := range []struct {
		wire               ID
		field, value, want string
	}{
		{OpenAIChat, "response_format", `{"type":"json_object"}`, "response_format"},
		{OpenAIChat, "response_format", `{"type":"text"}`, ""},
		{OpenAIResponses, "text", `{"format":{"type":"json_schema"}}`, "text.format"},
		{OpenAIResponses, "text", `{"format":{"type":"text"},"verbosity":"high"}`, "text.format"},
		{OpenAIResponses, "text", `{"format":{"type":"text"},"verbosity":null}`, ""},
	} {
		env, err := Inspect([]byte(`{"messages":[],"input":[],"` + tc.field + `":` + tc.value + `}`))
		if err != nil {
			t.Fatal(err)
		}
		_, err = wireCodec(tc.wire).DecodeRequest(env)
		var unsupported *UnsupportedError
		if tc.want == "" && err != nil || tc.want != "" && (!errors.As(err, &unsupported) || unsupported.Feature != tc.want) {
			t.Fatal(tc, err)
		}
		native, err := RewriteSame(tc.wire, env, nil)
		if err != nil || !bytes.Contains(native, []byte(tc.value)) {
			t.Fatal(tc, string(native), err)
		}
	}
}

func TestChatMultiChoiceConversion(t *testing.T) {
	e, err := Inspect([]byte(`{"model":"m","messages":[],"n":2}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = wireCodec(OpenAIChat).DecodeRequest(e)
	var unsupported *UnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatal("multi-choice Chat cannot convert to a single message", err)
	}
}

func TestImageSources(t *testing.T) {
	for _, url := range []string{"data:image/png;base64,YWJj", "http://example.test/a", "file:///tmp/a"} {
		body := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"` + url + `"}}]}]}`
		e, err := Inspect([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		req, err := wireCodec(OpenAIChat).DecodeRequest(e)
		if strings.HasPrefix(url, "file:") {
			if err == nil || ValidateRequest(OpenAIChat, e) == nil {
				t.Fatal("unsafe image URL")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		b, err := wireCodec(AnthropicMessages).EncodeRequest(req, EncodeOpts{})
		if err != nil {
			t.Fatal(err)
		}
		a, err := Inspect(b)
		if err != nil {
			t.Fatal(err)
		}
		req, err = wireCodec(AnthropicMessages).DecodeRequest(a)
		if err != nil || req.Messages[0].Blocks[0].Text != url {
			t.Fatal(string(b), err)
		}
	}
}

func TestSamplingAcrossProtocols(t *testing.T) {
	e, err := Inspect([]byte(`{"model":"m","messages":[],"max_completion_tokens":32,"stop":"end","tools":[{"type":"function","function":{"name":"f","strict":true}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req, err := wireCodec(OpenAIChat).DecodeRequest(e)
	if err != nil {
		t.Fatal(err)
	}
	b, err := wireCodec(AnthropicMessages).EncodeRequest(req, EncodeOpts{Defaults: map[string]json.RawMessage{"max_tokens": json.RawMessage("512")}})
	if err != nil || !bytes.Contains(b, []byte(`"max_tokens":32`)) || !bytes.Contains(b, []byte(`"stop_sequences":["end"]`)) {
		t.Fatal(string(b), err)
	}
	_, err = wireCodec(OpenAIResponses).EncodeRequest(req, EncodeOpts{})
	var unsupported *UnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatal("Responses cannot implement native stop sequences", err)
	}
	delete(req.Params, paramStop)
	b, err = wireCodec(OpenAIResponses).EncodeRequest(req, EncodeOpts{})
	if err != nil || !bytes.Contains(b, []byte(`"strict":true`)) || !bytes.Contains(b, []byte(`"max_output_tokens":32`)) {
		t.Fatal(string(b), err)
	}
	for _, tc := range []struct {
		wire       ID
		body, want string
	}{
		{OpenAIChat, `"messages":[],"max_tokens":128,"max_completion_tokens":null,"n":null`, `"max_output_tokens":128`},
		{AnthropicMessages, `"messages":[],"system":null,"max_tokens":128`, `"max_tokens":128`},
		{OpenAIResponses, `"input":[],"instructions":null,"max_output_tokens":128`, `"max_tokens":128`},
	} {
		raw := `{"tools":null,"tool_choice":null,"parallel_tool_calls":null,"store":null,"background":null,"stop":null,"temperature":null,` + tc.body + "}"
		env, err := Inspect([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		req, err := wireCodec(tc.wire).DecodeRequest(env)
		if err != nil {
			t.Fatal(err)
		}
		target := OpenAIChat
		if tc.wire == OpenAIChat {
			target = OpenAIResponses
		}
		out, err := wireCodec(target).EncodeRequest(req, EncodeOpts{})
		if err != nil || !bytes.Contains(out, []byte(tc.want)) || bytes.Contains(out, []byte("null")) || len(req.Messages) != 0 {
			t.Fatal(string(out), err)
		}
	}
}

func TestChatToolResultImageAfterParallelResults(t *testing.T) {
	const picture = "data:image/png;base64,YWJj"
	r := Request{Messages: []Message{
		{Role: roleUser, Blocks: []Block{{Kind: blockResult, ID: "a", Content: []Block{{Kind: blockImage, Text: picture}}}}},
		{Role: roleUser, Blocks: []Block{{Kind: blockResult, ID: "b", Content: []Block{{Kind: blockText, Text: "result"}}}}},
	}}
	raw, err := wireCodec(OpenAIChat).EncodeRequest(r, EncodeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	var chat struct {
		Messages []struct {
			Role    string
			Content json.RawMessage
		}
	}
	if err := json.Unmarshal(raw, &chat); err != nil {
		t.Fatal(err)
	}
	if len(chat.Messages) != 3 || chat.Messages[0].Role != roleTool || string(chat.Messages[0].Content) != `""` || chat.Messages[1].Role != roleTool || chat.Messages[2].Role != roleUser || !bytes.Contains(chat.Messages[2].Content, []byte(picture)) {
		t.Fatal(string(raw))
	}
}

func TestNullableResponseReferences(t *testing.T) {
	env, err := Inspect([]byte(`{"model":"m","input":"hi","store":false,"previous_response_id":null,"conversation":null}`))
	if err != nil {
		t.Fatal(err)
	}
	request, err := wireCodec(OpenAIResponses).DecodeRequest(env)
	if err != nil {
		t.Fatal(err)
	}
	cross, err := wireCodec(OpenAIChat).EncodeRequest(request, EncodeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	same, err := RewriteSame(OpenAIResponses, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range [][]byte{same, cross} {
		if bytes.Contains(body, []byte(`"previous_response_id"`)) || bytes.Contains(body, []byte(`"conversation"`)) {
			t.Fatal(string(body))
		}
	}
}
