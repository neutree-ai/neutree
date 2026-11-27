package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/neutree-ai/neutree/internal/icp/event"
	"github.com/neutree-ai/neutree/internal/icp/protocol"
	"github.com/neutree-ai/neutree/internal/icp/state"
)

func TestConvertedStreamMeteringAndTermination(t *testing.T) {
	const prefix = "/workspace/ws/endpoint/ep"
	peers := make(chan string, 3)
	kong := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peers <- r.RemoteAddr
		if r.URL.Path != chatPath {
			t.Error(r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set(internal+"Consumer-Id", "key")
		io.WriteString(w, "data: {\"id\":\"up-id\",\"model\":\"served\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\n")
		if r.URL.Query().Has("cut") {
			return
		}
		io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
		w.(http.Flusher).Flush()
		if r.URL.Query().Has("hang") {
			<-r.Context().Done()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}))
	defer kong.Close()
	app := appFor(t, Front, kong.URL)
	logs := make(recordSink, 1)
	app.Sink = logs
	for _, tc := range []struct{ path, body, end string }{
		{"/anthropic/v1/messages", `{"model":"v","messages":[{"role":"user","content":"hi"}],"stream":true}`, "event: message_stop"},
		{"/v1/responses?hang=1", `{"model":"v","input":"hi","stream":true}`, "event: response.completed"},
		{"/v1/responses?cut=1", `{"model":"v","input":"hi","stream":true}`, "event: error"},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, post(prefix+tc.path, tc.body).WithContext(ctx))
		err := ctx.Err()
		cancel()
		data := rec.Body.String()
		if err != nil || rec.Code != 200 || !strings.Contains(data, tc.end) {
			t.Fatalf("%s: %v, %d %s", tc.path, err, rec.Code, data)
		}
		log := <-logs
		ai := log["ai"].(Record)
		trace := ai["trace"].(Record)
		if strings.Contains(tc.path, "cut") {
			if strings.Contains(data, "event: response.completed") {
				t.Fatal(data)
			}
			continue
		}
		stats, ok := ai["statistics"].(Record)
		if !ok || trace["response_model"] != "served" || trace["finish_reason"] != "stop" || stats["meta"].(Record)["message_id"] != "up-id" || stats["usage"].(*protocol.Usage).Total != 10 {
			t.Fatal(ai)
		}
	}
	if a, b := <-peers, <-peers; a != b {
		t.Fatalf("upstream connection was not reused: %s, %s", a, b)
	}
}

func TestConfiguredUpstreamProtocol(t *testing.T) {
	const prefix = "/workspace/ws/endpoint/ep"
	const embeddings = prefix + "/v1/embeddings"
	kong := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == embeddings {
			io.WriteString(w, `{"data":[]}`)
			return
		}
		if r.URL.Path != prefix+"/v1/messages" || r.Header.Get("Anthropic-Version") == "" {
			t.Error(r.URL.Path, r.Header)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(429)
		io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"try later"}}`)
	}))
	defer kong.Close()
	app := appFor(t, Front, kong.URL)
	if err := app.Store.Apply(event.Event{Kind: state.RouteUpsert, Key: prefix, Value: state.Route{UpstreamProtocol: string(protocol.AnthropicMessages)}}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, post(embeddings, `{"model":"m","input":"hi"}`))
	if rec.Code != http.StatusOK || rec.Body.String() != `{"data":[]}` {
		t.Fatal(rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, post(chatPath, `{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	if rec.Code != 429 || !strings.Contains(rec.Body.String(), "try later") || strings.Contains(rec.Body.String(), `"type":"error"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	for _, wire := range []protocol.ID{protocol.AnthropicMessages, protocol.OpenAIResponses} {
		if err := app.Store.Apply(event.Event{Kind: state.RouteUpsert, Key: prefix, Value: state.Route{UpstreamProtocol: string(wire)}}); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, post(prefix+wire.Path(), `{"model":"m","store":true,"input":"x"}`))
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"code":"unsupported_feature"`) {
			t.Fatal(rec.Code, rec.Body.String())
		}
	}
	for _, tc := range []struct {
		wire, upstream, missing protocol.ID
	}{
		{protocol.OpenAIChat, "custom.v1", ""},
		{protocol.OpenAIChat, protocol.OpenAIChat, protocol.OpenAIChat},
		{protocol.OpenAIResponses, "", protocol.OpenAIChat},
		{protocol.OpenAIResponses, "", protocol.OpenAIResponses},
	} {
		app.Codecs = protocol.Codecs()
		delete(app.Codecs, tc.missing)
		if err := app.Store.Apply(event.Event{Kind: state.RouteUpsert, Key: prefix, Value: state.Route{UpstreamProtocol: string(tc.upstream)}}); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, post(prefix+tc.wire.Path(), `{"model":"m"}`))
		if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), `"code":"protocol_conversion_unavailable"`) {
			t.Fatal(tc, rec.Code, rec.Body.String())
		}
	}
}

func TestNativeAnthropicUpstream(t *testing.T) {
	const prefix = "/workspace/ws/endpoint/ep"
	const native = `{"type":"error","error":{"type":"overloaded_error","message":"busy"},"request_id":"upstream"}`
	for _, tc := range []struct {
		version, wantVersion        string
		status                      int
		body, wantType, wantMessage string
	}{
		{"", "2023-06-01", 429, `{"message":"quota"}`, "rate_limit_error", "quota"},
		{"client-version", "client-version", 529, native, "overloaded_error", "busy"},
	} {
		kong := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != prefix+"/v1/messages" || r.Header.Get("Anthropic-Version") != tc.wantVersion {
				t.Error(r.URL, r.Header)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(tc.status)
			io.WriteString(w, tc.body)
		}))
		app := appFor(t, Front, kong.URL)
		if err := app.Store.Apply(event.Event{Kind: state.RouteUpsert, Key: prefix, Value: state.Route{UpstreamProtocol: "anthropic.messages"}}); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		req := post(prefix+"/anthropic/v1/messages", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
		if tc.version != "" {
			req.Header.Set("Anthropic-Version", tc.version)
		}
		app.ServeHTTP(rec, req)
		kong.Close()
		var got struct {
			Type  string
			Error struct{ Type, Message string }
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if rec.Code != tc.status || got.Type != "error" || got.Error.Type != tc.wantType || got.Error.Message != tc.wantMessage || (tc.body == native && rec.Body.String() != native) {
			t.Fatal(rec.Code, rec.Body.String())
		}
	}
}
