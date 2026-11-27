package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/neutree-ai/neutree/internal/icp/quota"
)

type tinyReader struct {
	r io.Reader
	n int
}

func (t *tinyReader) Read(p []byte) (int, error) {
	if len(p) > t.n {
		p = p[:t.n]
	}

	return t.r.Read(p)
}

func TestResponsesRequestToAnthropicStreamAndBackWithParallelTools(t *testing.T) {
	respCodec := ResponsesCodec{}
	antCodec := AnthropicCodec{}

	clientReqJSON := `{
		"model": "smart-v1",
		"instructions": "Be concise.",
		"stream": true,
		"tool_choice": {"type":"function","name":"get_weather"},
		"tools": [{"type":"function","name":"get_weather","description":"Weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}],
		"input": [
			{"type":"message","role":"user","content":[{"type":"input_text","text":"Weather in Tokyo and Paris?"}]}
		]
	}`

	irReq, err := respCodec.DecodeRequest(strings.NewReader(clientReqJSON))
	if err != nil {
		t.Fatal(err)
	}

	antReqBytes, err := antCodec.EncodeRequest(irReq, "claude-sonnet-4")
	if err != nil {
		t.Fatal(err)
	}

	var antWire map[string]any
	if err := json.Unmarshal(antReqBytes, &antWire); err != nil {
		t.Fatal(err)
	}
	if antWire["model"] != "claude-sonnet-4" || antWire["system"] != "Be concise." || antWire["stream"] != true || !strings.Contains(string(antReqBytes), `"tool_choice":{"name":"get_weather","type":"tool"}`) {
		t.Fatalf("unexpected encoded Anthropic request: %s", antReqBytes)
	}

	// Simulated Anthropic upstream SSE stream with split chunks, thinking signature, text, 2 parallel tool_use blocks, and split usage.
	antSSE := strings.Join([]string{
		"event: message_start\r\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_01\",\"model\":\"claude-sonnet-4\",\"usage\":{\"input_tokens\":42,\"cache_read_input_tokens\":10}}}\r\n\r\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"Checking two cities.\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"ant-sig-xyz\"}}\n\n",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"Let me look \"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"both up.\"}}\n\n",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":2,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_1\",\"name\":\"get_weather\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":2,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"city\\\":\\\"Tok\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":2,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"yo\\\"}\"}}\n\n",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":2}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":3,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_2\",\"name\":\"get_weather\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":3,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"city\\\":\\\"Paris\\\"}\"}}\n\n",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":3}\n\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":18}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	}, "")

	var downstreamBuf bytes.Buffer
	enc := respCodec.NewStreamEncoder(&downstreamBuf, irReq)
	var capturedUsage []quota.Measure

	if err := antCodec.DecodeStream(&tinyReader{r: strings.NewReader(antSSE), n: 7}, func(ev StreamEvent) error {
		if ev.Type == EventUsage {
			capturedUsage = ev.Usage
		}

		return enc.WriteEvent(ev)
	}); err != nil {
		t.Fatal(err)
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}

	// Anthropic input_tokens (42) + cache_read_input_tokens (10) = 52 total input tokens; + output(18) = 70 billable units without double-counting sub-breakdowns.
	if got := quota.BillableUnits(capturedUsage); got != 70 {
		t.Fatalf("expected BillableUnits=70 from normalized Anthropic usage, got %d (%+v)", got, capturedUsage)
	}

	outSSE := downstreamBuf.String()
	// Verify reasoning output_index=0 and message output_index=1 do NOT collide!
	for _, want := range []string{
		"event: response.created",
		`"output_index":0`, `"type":"reasoning"`,
		`"output_index":1`, `"type":"message"`,
		`"output_index":2`, `"output_index":3`,
		"event: response.output_text.delta",
		"event: response.function_call_arguments.delta",
		"event: response.completed",
	} {
		if !strings.Contains(outSSE, want) {
			t.Fatalf("missing Responses SSE fragment %q in:\n%s", want, outSSE)
		}
	}
	// Anthropic signature must NOT leak into OpenAI Responses encrypted_content.
	if strings.Contains(outSSE, "ant-sig-xyz") {
		t.Fatalf("foreign Anthropic thinking signature leaked into Responses stream:\n%s", outSSE)
	}

	// Round-trip decode the emitted Responses stream back into IR and fold to verify fidelity!
	var roundTripEvents []StreamEvent
	if err := respCodec.DecodeStream(&tinyReader{r: bytes.NewReader(downstreamBuf.Bytes()), n: 11}, func(ev StreamEvent) error {
		roundTripEvents = append(roundTripEvents, ev)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	folded := FoldEvents("smart-v1", roundTripEvents)
	if folded.FinishReason != "tool_calls" || len(folded.Content) < 3 {
		t.Fatalf("unexpected folded response: %+v", folded)
	}
}

func TestOpenAIChatIncludeUsageInjectionAndStripping(t *testing.T) {
	chat := ChatCodec{}

	// Client did NOT set stream_options.include_usage.
	irReq, err := chat.DecodeRequest(strings.NewReader(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if irReq.ClientRequestedUsage {
		t.Fatal("ClientRequestedUsage should be false")
	}

	// Outbound upstream request MUST inject stream_options.include_usage=true.
	upBytes, err := chat.EncodeRequest(irReq, "qwen-72b")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(upBytes), `"include_usage":true`) {
		t.Fatalf("expected stream_options.include_usage=true in upstream payload: %s", upBytes)
	}

	upstreamSSE := strings.Join([]string{
		`data: {"id":"chatcmpl-1","model":"qwen-72b","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello!"}}]}`,
		`data: {"id":"chatcmpl-1","model":"qwen-72b","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"chatcmpl-1","model":"qwen-72b","choices":[],"usage":{"prompt_tokens":11,"completion_tokens":4,"total_tokens":15}}`,
		`data: [DONE]`,
		"",
	}, "\n\n")

	var clientOut bytes.Buffer
	enc := chat.NewStreamEncoder(&clientOut, irReq)
	var gatewayMeasures []quota.Measure
	if err := chat.DecodeStream(&tinyReader{r: strings.NewReader(upstreamSSE), n: 9}, func(ev StreamEvent) error {
		if ev.Type == EventUsage {
			gatewayMeasures = ev.Usage
		}

		return enc.WriteEvent(ev)
	}); err != nil {
		t.Fatal(err)
	}
	_ = enc.Close()

	// Gateway captured 15 tokens, while downstream client stream stripped the unrequested usage chunk!
	if quota.BillableUnits(gatewayMeasures) != 15 {
		t.Fatalf("expected gateway to capture 15 billable tokens, got %+v", gatewayMeasures)
	}
	if strings.Contains(clientOut.String(), `"prompt_tokens"`) || !strings.Contains(clientOut.String(), "data: [DONE]") {
		t.Fatalf("expected usage tail chunk stripped for client that did not request it, got:\n%s", clientOut.String())
	}
}

func TestThinkingSignatureIsolationAndUnsupportedStatefulFeatures(t *testing.T) {
	// 1. Anthropic signed thinking replayed to OpenAI Chat strips signature; replayed to Anthropic preserves signature.
	ant := AnthropicCodec{}
	chat := ChatCodec{}
	resp := ResponsesCodec{}

	antHistory := `{"model":"m","max_tokens":256,"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"step1","signature":"sig-secret-1"},{"type":"text","text":"answer"}]},{"role":"user","content":"follow up"}]}`
	irReq, err := ant.DecodeRequest(strings.NewReader(antHistory))
	if err != nil {
		t.Fatal(err)
	}

	sameVendor, err := ant.EncodeRequest(irReq, "claude-4")
	if err != nil || !strings.Contains(string(sameVendor), "sig-secret-1") {
		t.Fatalf("same-vendor Anthropic request lost signature: %s (%v)", sameVendor, err)
	}

	crossVendor, err := chat.EncodeRequest(irReq, "gpt-4o")
	if err != nil || strings.Contains(string(crossVendor), "sig-secret-1") {
		t.Fatalf("cross-vendor Chat request leaked Anthropic signature: %s (%v)", crossVendor, err)
	}

	// 2. Non-streaming translation Chat response -> Anthropic response & Responses response.
	chatRespJSON := `{"id":"chatcmpl-9","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"Done","tool_calls":[{"id":"c1","type":"function","function":{"name":"ping","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":8,"completion_tokens":3,"total_tokens":11}}`
	irResp, err := chat.DecodeResponse(strings.NewReader(chatRespJSON))
	if err != nil {
		t.Fatal(err)
	}
	antRespBytes, err := ant.EncodeResponse(irResp, irReq)
	if err != nil || !strings.Contains(string(antRespBytes), `"stop_reason":"tool_use"`) {
		t.Fatalf("unexpected non-stream Anthropic response: %s (%v)", antRespBytes, err)
	}
	respBytes, err := resp.EncodeResponse(irResp, irReq)
	if err != nil || !strings.Contains(string(respBytes), `"type":"function_call"`) {
		t.Fatalf("unexpected non-stream Responses payload: %s (%v)", respBytes, err)
	}

	// 3. Stateful features (previous_response_id, store:true, web_search_preview) return 400 unsupported_feature on stateless targets.
	cases := []string{
		`{"model":"m","previous_response_id":"resp_prev","input":"continue"}`,
		`{"model":"m","store":true,"input":"save me"}`,
		`{"model":"m","tools":[{"type":"web_search_preview"}],"input":"search"}`,
	}
	for _, raw := range cases {
		req, err := resp.DecodeRequest(strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		// Allowed when target is openai_responses:
		if _, err := resp.EncodeRequest(req, "gpt-4.1"); err != nil {
			t.Fatalf("expected openai_responses target to allow stateful feature, got %v", err)
		}
		// Rejected with 400 unsupported_feature when target is openai_chat or anthropic_messages:
		_, err = chat.EncodeRequest(req, "qwen-72b")
		var uf *UnsupportedFeatureError
		if !errors.As(err, &uf) || uf.StatusCode() != 400 || !strings.Contains(string(uf.JSON()), "unsupported_feature") {
			t.Fatalf("expected 400 UnsupportedFeatureError for %s, got %v", raw, err)
		}
	}
}
