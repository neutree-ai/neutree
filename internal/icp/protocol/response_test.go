package protocol

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestResponseConversions(t *testing.T) {
	inputs := map[ID]string{
		OpenAIChat:        `{"id":"up","model":"served","choices":[{"message":{"role":"assistant","reasoning_content":"thinking","content":"answer","tool_calls":[{"id":"call_A","type":"function","function":{"name":"search","arguments":"{}"}},{"id":"call_B","type":"function","function":{"name":"count","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":9,"completion_tokens":8,"prompt_tokens_details":{"cached_tokens":3,"cache_write_tokens":2}}}`,
		AnthropicMessages: `{"id":"up","type":"message","role":"assistant","model":"served","content":[{"type":"thinking","thinking":"thinking","signature":"native"},{"type":"text","text":"answer"},{"type":"tool_use","id":"call_A","name":"search","input":{}},{"type":"tool_use","id":"call_B","name":"count","input":{}}],"stop_reason":"tool_use","usage":{"input_tokens":4,"output_tokens":8,"cache_read_input_tokens":3,"cache_creation_input_tokens":2}}`,
		OpenAIResponses:   `{"id":"up","model":"served","status":"completed","output":[{"type":"reasoning","summary":[{"type":"summary_text","text":"thinking"}],"encrypted_content":"secret"},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]},{"type":"function_call","id":"fc_x","call_id":"call_A","name":"search","arguments":"{}"},{"type":"function_call","id":"fc_y","call_id":"call_B","name":"count","arguments":"{}"}],"usage":{"input_tokens":9,"output_tokens":8,"input_tokens_details":{"cached_tokens":3,"cache_write_tokens":2}}}`,
	}
	for src, in := range inputs {
		for dst := range inputs {
			if src == dst {
				continue
			}
			t.Run(string(src)+"/"+string(dst), func(t *testing.T) {
				r, err := wireCodec(src).DecodeResponse([]byte(in))
				if err != nil {
					t.Fatal(err)
				}
				if r.ID != "up" || r.Model != "served" {
					t.Fatal(r)
				}
				wire, err := wireCodec(dst).EncodeResponse(r, EncodeOpts{ID: "req", Model: "virtual", Created: 42})
				if err != nil {
					t.Fatal(err)
				}
				var out struct {
					Output []struct {
						Type, ID string
						CallID   string `json:"call_id"`
					}
					Usage struct {
						Input int `json:"input_tokens"`
					}
				}
				if err := json.Unmarshal(wire, &out); err != nil {
					t.Fatal(err)
				}
				if dst == AnthropicMessages && out.Usage.Input != 4 {
					t.Fatalf("wrong uncached input: %s", wire)
				}
				if dst == OpenAIResponses {
					for _, i := range out.Output {
						if i.Type == "function_call" && (i.CallID == "" || i.ID == i.CallID || i.ID == "") {
							t.Fatal(i)
						}
					}
				}
				got, err := wireCodec(dst).DecodeResponse(wire)
				if err != nil {
					t.Fatal(err)
				}
				if got.FinishReason != "tool_calls" || got.ID != "req" || got.Model != "virtual" || got.Usage == nil || got.Usage.Prompt != 9 || got.Usage.Completion != 8 || got.Usage.Total != 17 {
					t.Fatalf("response metadata lost: %+v %s", got, wire)
				}
				var text, think string
				var ids []string
				for _, b := range got.Blocks {
					switch b.Kind {
					case "tool_use":
						ids = append(ids, b.ID)
					case "text":
						text += b.Text
					case "thinking":
						think += b.Text
						if b.Signature != "" {
							t.Fatal("foreign signature crossed protocol", b)
						}
					}
				}
				if !reflect.DeepEqual(ids, []string{"call_A", "call_B"}) || text != "answer" || think != "thinking" {
					t.Fatalf("%s", wire)
				}
			})
		}
	}
	for _, tc := range []struct {
		args, want string
	}{
		{"", "{}"}, {`{"x":`, `{"raw":"{\"x\":"}`}, {`[1]`, `{"raw":"[1]"}`},
	} {
		block := Block{Kind: blockCall, Data: json.RawMessage(tc.args)}
		r := Response{Blocks: []Block{block}, FinishReason: finishLength, Usage: &Usage{Completion: 7, Total: 7}}
		wire, err := wireCodec(AnthropicMessages).EncodeResponse(r, EncodeOpts{})
		if err != nil {
			t.Fatal(err)
		}
		got, err := wireCodec(AnthropicMessages).DecodeResponse(wire)
		if err != nil || len(got.Blocks) != 1 || string(got.Blocks[0].Data) != tc.want || got.FinishReason != finishLength || got.Usage.Total != 7 {
			t.Fatal(string(wire), err)
		}
		if tc.args != "" {
			_, err := wireCodec(AnthropicMessages).EncodeRequest(Request{Messages: []Message{{roleAssistant, r.Blocks}}}, EncodeOpts{})
			if err == nil {
				t.Fatal("accepted malformed tool history", tc.args)
			}
		}
	}
	wire, err := wireCodec(AnthropicMessages).EncodeResponse(Response{}, EncodeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	empty, err := wireCodec(AnthropicMessages).DecodeResponse(wire)
	if err != nil || len(empty.Blocks) != 1 || empty.Blocks[0].Kind != blockText || empty.Blocks[0].Text != "" {
		t.Fatal(string(wire), err)
	}
}

func TestInclusiveUsageDetails(t *testing.T) {
	for _, tc := range []struct {
		wire                ID
		in                  string
		read, write, reason int64
	}{
		{OpenAIChat, `{"prompt_tokens":4,"completion_tokens":2,"total_tokens":100,"prompt_tokens_details":{"cached_tokens":5,"cache_write_tokens":3},"completion_tokens_details":{"reasoning_tokens":9}}`, 4, 0, 2},
		{OpenAIResponses, `{"input_tokens":4,"output_tokens":2,"input_tokens_details":{"cache_write_tokens":0}}`, -1, 0, -1},
	} {
		u, err := decodeUsage(tc.wire, []byte(tc.in), nil)
		if err != nil {
			t.Fatal(err)
		}
		if u.Total != 6 {
			t.Fatal(u)
		}
		for _, n := range []struct {
			p    *int64
			want int64
		}{{u.CacheRead, tc.read}, {u.CacheWrite, tc.write}, {u.Reasoning, tc.reason}} {
			if n.want < 0 && n.p != nil || n.want >= 0 && (n.p == nil || *n.p != n.want) {
				t.Fatal(tc, u)
			}
		}
	}
}

func TestObserveNativeResponse(t *testing.T) {
	for wire, raw := range map[ID]string{
		AnthropicMessages: `{"id":"up","model":"served","content":[{"type":"vendor_custom"}],"stop_reason":"max_tokens","usage":{"input_tokens":2,"cache_creation_input_tokens":1,"output_tokens":4}}`,
		OpenAIResponses:   `{"id":"up","model":"served","output":[{"type":"vendor_custom"}],"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":3,"output_tokens":4}}`,
	} {
		r, err := ObserveResponse(wire, []byte(raw))
		if err != nil || r.ID != "up" || r.Model != "served" || r.Usage == nil || r.Usage.Total != 7 || r.FinishReason != finishLength {
			t.Fatal(wire, r, err)
		}
	}
}
