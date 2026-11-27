package protocol

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
)

func TestInspectAndRewrite(t *testing.T) {
	for _, raw := range []string{`{"mo\u0064el":"a","model":"b"}`, `{"Model":"a","model":"b"}`, `{"messages":[],"messages":[]}`, `{}{}`} {
		if _, err := Inspect([]byte(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	raw := []byte(`{ "model": "v", "temperature":0, "max_completion_tokens":0, "extra":false, "messages":[{"role":"user","content":"hi"}] }`)
	e, err := Inspect(raw)
	if err != nil {
		t.Fatal(err)
	}
	out, err := RewriteSame(OpenAIChat, e, map[string]json.RawMessage{"max_tokens": []byte("256"), "temperature": []byte("0.8"), "extra": []byte("true")})
	if err != nil || &out[0] != &raw[0] || !e.HasPrefix || len(e.BlockHashes) == 0 {
		t.Fatal("lost original bytes or affinity", err)
	}
	e, err = Inspect([]byte(`{"model":"v","stream":true,"stream_options":{"other":4}}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err = RewriteSame(OpenAIChat, e, nil)
	if err != nil || e.ClientUsage || !bytes.Contains(out, []byte(`"other":4`)) || !bytes.Contains(out, []byte(`"include_usage":true`)) {
		t.Fatal(string(out), err)
	}
	for _, tc := range []struct {
		wire                   ID
		body, defaultKey, want string
	}{
		{OpenAIChat, `{"max_completion_tokens":null,"temperature":null}`, maxTokens, `{"max_tokens":128,"temperature":null}`},
		{OpenAIChat, `{"max_tokens":null}`, completionMaxTokens, `{"max_completion_tokens":128}`},
		{OpenAIChat, `{"max_tokens":0,"max_completion_tokens":null}`, "temperature", `{"max_tokens":0,"temperature":128}`},
		{OpenAIChat, `{ "max_tokens":0, "max_completion_tokens":null }`, completionMaxTokens, `{ "max_tokens":0, "max_completion_tokens":null }`},
		{AnthropicMessages, `{"temperature":null,"max_tokens":null}`, "temperature", `{"max_tokens":4096,"temperature":128}`},
	} {
		env, err := Inspect([]byte(tc.body))
		if err != nil {
			t.Fatal(err)
		}
		out, err := RewriteSame(tc.wire, env, map[string]json.RawMessage{tc.defaultKey: []byte("128")})
		if err != nil || string(out) != tc.want {
			t.Fatalf("%s → %s, want %s: %v", tc.body, out, tc.want, err)
		}
	}
}

func TestSSEWireBoundaries(t *testing.T) {
	wire := "\ufeff:heartbeat\r\n\r\nevent: x\rdata: one\ndata: two\r\n\r"
	var got bytes.Buffer
	var data []string
	err := ScanSSE(iotest.OneByteReader(strings.NewReader(wire)), func(f Frame) error {
		got.Write(f.Raw)
		if f.HasData {
			data = append(data, f.Name+"="+string(f.Data))
		}
		return nil
	})
	if err != nil || got.String() != wire || strings.Join(data, "") != "x=one\ntwo" {
		t.Fatal(data, got.String(), err)
	}
	count := 0
	err = ScanSSE(strings.NewReader("data: complete\n\ndata: partial"), func(Frame) error { count++; return nil })
	if err != io.ErrUnexpectedEOF || count != 1 {
		t.Fatal(count, err)
	}
}

func TestSSELimits(t *testing.T) {
	one := strings.Repeat("x", MaxFrame-2) + "\n\n"
	seen := 0
	err := ScanSSE(io.MultiReader(strings.NewReader(one), strings.NewReader(one), strings.NewReader(one), strings.NewReader(one), strings.NewReader("x")), func(Frame) error { seen++; return nil })
	if seen != 4 || err == nil || !strings.Contains(err.Error(), "16 MiB") {
		t.Fatal(seen, err)
	}
	err = ScanSSE(strings.NewReader("x"+one), func(Frame) error { t.Fatal("oversize frame delivered"); return nil })
	if err == nil || !strings.Contains(err.Error(), "4 MiB") {
		t.Fatal(err)
	}
}

func TestSystemAndInputAffinity(t *testing.T) {
	// Equivalent roles across the three APIs should select the same prefix.
	var want Envelope
	for _, body := range []string{
		`{"messages":[{"role":"developer","content":"system"},{"role":"user","content":"question"}]}`,
		`{"system":"system","messages":[{"role":"user","content":"question"}]}`,
		`{"system":null,"instructions":"system","input":"question"}`,
		`{"instructions":null,"input":[{"type":"message","role":"system","content":"system"},{"role":"user","content":"question"}]}`,
	} {
		got, err := Inspect([]byte(body))
		if err != nil || !got.HasPrefix || len(got.BlockHashes) == 0 {
			t.Fatal(body, err)
		}
		if want.HasPrefix && (got.PrefixHash != want.PrefixHash || !slices.Equal(got.BlockHashes, want.BlockHashes)) {
			t.Fatal(body, got.PrefixHash, want.PrefixHash)
		}
		want = got
	}
}

func TestNativeResponsesDefaults(t *testing.T) {
	e, err := Inspect([]byte(`{"input":"hi","tools":[{"type":"function","name":"f","description":"preserve me"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := RewriteSame(OpenAIResponses, e, nil)
	if err != nil || !bytes.Contains(out, []byte(`"store":false`)) || !bytes.Contains(out, []byte(`"strict":false`)) || !bytes.Contains(out, []byte("preserve me")) {
		t.Fatal(string(out), err)
	}
}
