package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neutree-ai/neutree/internal/icp/event"
	"github.com/neutree-ai/neutree/internal/icp/protocol"
	"github.com/neutree-ai/neutree/internal/icp/router"
	"github.com/neutree-ai/neutree/internal/icp/state"
)

func appFor(t *testing.T, mode, url string) *Server {
	t.Helper()
	o, err := ParseOptions([]string{"--mode=" + mode})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &state.Store{}
	if err = cfg.Apply(event.Event{Kind: state.ConfigReplace, Value: state.ConfigSnapshot{Kong: url}}); err != nil {
		t.Fatal(err)
	}
	app := NewServer(o, cfg, state.NewRuntime(1))
	t.Cleanup(app.transport.CloseIdleConnections)
	return app
}

const chatPath = "/workspace/ws/endpoint/ep/v1/chat/completions"

func post(path, body string) *http.Request {
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}

func TestFrontOwnsMeteringAndIdentity(t *testing.T) {
	for _, tc := range []struct {
		path        string
		status      int
		clientUsage bool
	}{
		{chatPath, 200, false}, {chatPath, 429, false},
		{"/workspace/ws/endpoint/ep/v1/completions", 200, false},
		{"/workspace/ws/endpoint/ep/v1/completions", 200, true},
	} {
		status := tc.status
		t.Run(fmt.Sprint(tc.path, "/", status, "/", tc.clientUsage), func(t *testing.T) {
			var calls atomic.Int32
			kong := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get(internal+"Token") != "trusted" || r.Header.Get(internal+"Model") != "client-model" || r.Header.Get("kong_apikey") != "" || r.URL.RawQuery != "keep=1" {
					t.Error("incorrect outbound auth or routing dimensions")
				}
				if r.Header.Get("Authorization") != "Bearer client" {
					t.Error("lost client credentials")
				}
				var env struct {
					StreamOptions struct {
						Include bool `json:"include_usage"`
					} `json:"stream_options"`
				}
				if err := json.NewDecoder(r.Body).Decode(&env); err != nil || !env.StreamOptions.Include {
					t.Error("missing upstream usage request", err)
				}
				w.Header().Set(internal+"Consumer-Id", "consumer")
				w.Header().Set(internal+"Selected-Upstream", "selected")
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(status)
				if status == 200 {
					io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":3,\"total_tokens\":15}}\n\ndata: [DONE]\n\n")
				}
			}))
			defer kong.Close()
			records := make(chan map[string]any, 1)
			vector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var rec map[string]any
				if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
					t.Error(err)
				}
				records <- rec
			}))
			defer vector.Close()
			app := appFor(t, Front, kong.URL)
			app.Options.Token = "trusted"
			app.Sink = NewVectorSink(vector.URL, 2)
			payload := `{"model":"client-model","stream":true}`
			if tc.clientUsage {
				payload = `{"model":"client-model","stream":true,"stream_options":{"include_usage":true}}`
			}
			req := post(tc.path+"?kong_apikey=bad&keep=1", payload)
			req.Header.Set("kong_apikey", "bad")
			req.Header.Set(internal+"Token", "spoof")
			req.Header.Set("Authorization", "Bearer client")
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, req)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := app.Sink.Drain(ctx); err != nil {
				t.Fatal(err)
			}
			if rec.Code != status || calls.Load() != 1 || rec.Header().Get(internal+"Consumer-Id") != "" {
				t.Fatal(rec.Code, calls.Load(), rec.Header())
			}
			if status == 200 && (strings.Contains(rec.Body.String(), "prompt_tokens") != tc.clientUsage || !strings.Contains(rec.Body.String(), "[DONE]")) {
				t.Fatal(rec.Body.String())
			}
			select {
			case r := <-records:
				ai := r["ai"].(map[string]any)
				u, ok := ai["statistics"]
				if ok != (status == 200) {
					t.Fatal(ai)
				}
				if ok && u.(map[string]any)["usage"].(map[string]any)["total_tokens"] != float64(15) {
					t.Fatal(u)
				}
				if r["consumer"].(map[string]any)["custom_id"] != "consumer" || r["request"].(map[string]any)["uri"] != tc.path+"?keep=1" {
					t.Fatal(r)
				}
			case <-ctx.Done():
				t.Fatal("missing Vector record")
			}
		})
	}
}

func TestMultipartGuardsBeforeForwarding(t *testing.T) {
	var src bytes.Buffer
	mw := multipart.NewWriter(&src)
	mw.WriteField("model", "good")
	f, _ := mw.CreateFormFile("audio", "clip.raw")
	f.Write(bytes.Repeat([]byte("x"), sniffBytes+32))
	mw.WriteField("model", "bad")
	mw.Close()
	model, body, _, err := multipartBody(io.NopCloser(&src), mw.Boundary())
	if err != nil || model != "good" {
		t.Fatal(model, err)
	}
	defer body.Close()
	b, err := io.ReadAll(body)
	if err == nil || bytes.Count(b, []byte(`name="model"`)) != 1 || bytes.Contains(b, []byte("\r\nbad\r\n")) {
		t.Fatal("late model escaped validation", err)
	}
	var late bytes.Buffer
	mw = multipart.NewWriter(&late)
	f, _ = mw.CreateFormFile("audio", "clip")
	f.Write(bytes.Repeat([]byte("x"), sniffBytes))
	mw.WriteField("model", "late")
	mw.Close()
	if _, _, _, err = multipartBody(io.NopCloser(&late), mw.Boundary()); err == nil {
		t.Fatal("accepted late model")
	}
}

type orderedPicker struct{}

func (orderedPicker) Pick(s router.RouteSignals, v state.RuntimeView) (state.PodTarget, error) {
	for _, p := range v.Pods {
		if p.UID != s.ExcludeUID {
			return p, nil
		}
	}
	return state.PodTarget{}, router.ErrNoBackend
}

type recorderHook struct {
	id    string
	order *[]string
}

func (h recorderHook) add(s string)                    { *h.order = append(*h.order, h.id+s) }
func (h recorderHook) OnIngress(*RequestContext) error { return nil }
func (h recorderHook) OnRequest(*RequestContext) error { return nil }
func (h recorderHook) OnComplete(*RequestContext)      { h.add("complete") }
func (h recorderHook) OnAttemptStart(*Attempt) error   { h.add("start"); return nil }
func (h recorderHook) OnAttemptResponse(*Attempt, *http.Response) error {
	h.add("response")
	return nil
}

func (h recorderHook) OnAttemptDone(*Attempt) { h.add("done") }

func TestClusterRetryCommitAndHooks(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprint(committed), func(t *testing.T) {
			var hits atomic.Int32
			first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for k := range r.Header {
					if strings.HasPrefix(k, internal) {
						t.Error("Pod received internal header", k)
					}
				}
				if r.URL.Path != "/v1/completions" {
					t.Error(r.URL.Path)
				}
				w.Header().Set(internal+"Secret", "pod")
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(200)
				w.(http.Flusher).Flush()
				if committed {
					io.WriteString(w, "data: {\"choices\":[]}\n\n")
					w.(http.Flusher).Flush()
					io.WriteString(w, "data: unfinished")
					w.(http.Flusher).Flush()
					panic(http.ErrAbortHandler)
				}
				<-r.Context().Done()
			}))
			defer first.Close()
			next := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "data: [DONE]\n\n")
			}))
			defer next.Close()
			s := appFor(t, Cluster, "")
			s.Options.First = 100 * time.Millisecond
			s.Pickers["new"] = orderedPicker{}
			ps := []state.PodTarget{{UID: "a", URL: first.URL, RoutingLogic: "new"}, {UID: "b", URL: next.URL, RoutingLogic: "new"}}
			s.Runtime.Apply(event.Event{Kind: state.EndpointTargets, Key: "ws/ep", Value: ps, ExpiresAt: time.Now().Add(time.Hour)})
			var order []string
			for _, id := range []string{"1", "2"} {
				s.Plugins = append(s.Plugins, func(*RequestContext) RequestHook { return recorderHook{id, &order} })
			}
			rec := httptest.NewRecorder()
			r := post("/ws/ep/v1/completions", `{"model":"v","stream":true}`)
			r.Header.Set(internal+"Prefix-Hash", "f")
			var aborted any
			func() {
				defer func() { aborted = recover() }()
				s.ServeHTTP(rec, r)
			}()
			if committed && aborted != http.ErrAbortHandler || !committed && aborted != nil {
				t.Fatalf("abort = %v", aborted)
			}
			if rec.Header().Get(internal+"Secret") != "" || rec.Code != 200 {
				t.Fatal(rec.Header(), rec.Code)
			}
			text := rec.Body.String()
			if committed {
				if hits.Load() != 0 || text != "data: {\"choices\":[]}\n\ndata: unfinished" {
					t.Fatal(text, hits.Load())
				}
			} else if hits.Load() != 1 || !strings.Contains(text, "[DONE]") {
				t.Fatal(text, hits.Load())
			}
			want := []string{"1start", "2start"}
			if !committed {
				want = append(want, "2done", "1done", "1start", "2start")
			}
			want = append(want, "1response", "2response", "2done", "1done", "2complete", "1complete")
			if !slices.Equal(order, want) {
				t.Fatal(order)
			}
		})
	}
}

func TestDisconnectReleasesBackend(t *testing.T) {
	canceled := make(chan struct{})
	pod := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, ":live\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(canceled)
	}))
	defer pod.Close()
	s := appFor(t, Cluster, "")
	s.Runtime.Apply(event.Event{Kind: state.EndpointTargets, Key: "ws/ep", Value: []state.PodTarget{{UID: "p", URL: pod.URL}}, ExpiresAt: time.Now().Add(time.Hour)})
	load := s.Runtime.View("ws/ep", time.Now()).Pods[0].Load
	server := httptest.NewServer(s)
	defer server.Close()
	resp, err := http.Post(server.URL+"/ws/ep/v1/chat/completions", "application/json", strings.NewReader(`{"model":"v","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("Pod still running")
	}
	server.Close()
	if load.Inflight.Load() != 0 {
		t.Fatal("backend slot leaked")
	}
}

func TestSessionHeaderPrecedence(t *testing.T) {
	app := appFor(t, Cluster, "")
	app.Options.SessionKey = "Conversation"
	for _, tc := range []struct{ method, path, first, configured, alias, want string }{
		{"GET", "/", "", "", "public", "public"},
		{"POST", "/v1/embeddings", "", "configured", "alias", "configured"},
		{"POST", "/v1/rerank", "trusted", "configured", "alias", "trusted"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("Conversation", tc.configured)
		req.Header.Set("X-Session-ID", tc.alias)
		var x exchange
		x.Signals.Session = tc.first
		if err := app.prepare(&x, req, tc.path, "", false); err != nil || x.Signals.Session != tc.want {
			t.Errorf("%s: session %q, err %v", tc.path, x.Signals.Session, err)
		}
	}
	// A caller's session header still needs local extraction of the prompt prefix.
	req := post("/v1/chat/completions", `{"messages":[{"role":"user","content":"hello"}]}`)
	req.Header.Set("X-Session-ID", "client")
	var x exchange
	if err := app.prepare(&x, req, req.URL.Path, "", false); err != nil || !x.Signals.HasPrefix || x.Signals.Session != "client" {
		t.Fatalf("direct Chat signals %+v, err %v", x.Signals, err)
	}

}

type rejectingHook struct {
	recorderHook
	phase string
}

func (h rejectingHook) OnIngress(*RequestContext) error {
	if h.phase == "ingress" {
		return errors.New("rejected ingress")
	}
	return nil
}
func (h rejectingHook) OnAttemptStart(*Attempt) error {
	h.add("start")
	return errors.New("rejected attempt")
}
func TestRejectedHookOnlyUnwindsEnteredHooks(t *testing.T) {
	for _, tc := range []struct {
		phase string
		want  []string
	}{
		{"ingress", []string{"Acomplete"}},
		{"attempt", []string{"Astart", "Bstart", "Adone", "Bcomplete", "Acomplete"}},
	} {
		t.Run(tc.phase, func(t *testing.T) {
			var order []string
			app := appFor(t, L0, "")
			app.Plugins = []Plugin{
				func(*RequestContext) RequestHook { return nil },
				func(*RequestContext) RequestHook { return recorderHook{"A", &order} },
				func(*RequestContext) RequestHook { return rejectingHook{recorderHook{"B", &order}, tc.phase} },
			}
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, httptest.NewRequest("GET", "/models", nil))
			if rec.Code != 502 || rec.Header().Get("X-Request-Id") == "" || !slices.Equal(order, tc.want) {
				t.Fatalf("status %d, callbacks %v", rec.Code, order)
			}
		})
	}
}
func TestZeroUsageEmitsTraceOnly(t *testing.T) {
	x := exchange{RequestContext: RequestContext{Request: httptest.NewRequest("GET", "/", nil), Status: 200}, consumer: "consumer", usage: &protocol.Usage{}}
	ai := x.record()["ai"].(Record)
	if _, ok := ai["statistics"]; ok || ai["trace"] == nil {
		t.Fatal(ai)
	}
}

func TestFrontSSETerminalDrainsConnection(t *testing.T) {
	const done = "data: [DONE]\n\n"
	peers := make(chan string, 3)
	kong := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peers <- r.RemoteAddr
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, done)
		w.(http.Flusher).Flush()
		if r.URL.Query().Has("hang") {
			<-r.Context().Done()
		} else {
			// Deliver the chunked terminator in a later read than the SSE terminator.
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer kong.Close()
	app := appFor(t, Front, kong.URL)
	for i, query := range []string{"", "", "?hang=1"} {
		rec := httptest.NewRecorder()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		app.ServeHTTP(rec, post(chatPath+query, `{"model":"m","stream":true}`).WithContext(ctx))
		err := ctx.Err()
		cancel()
		if err != nil || rec.Code != 200 || rec.Body.String() != done {
			t.Fatalf("request %d: %v, %d, %q", i, err, rec.Code, rec.Body.String())
		}
	}
	first, second := <-peers, <-peers
	if first != second {
		t.Fatalf("normal streams used different upstream connections: %s, %s", first, second)
	}
}

func TestFrontInterruptedFrame(t *testing.T) {
	kong := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, ":live\n\ndata: incomplete")
	}))
	defer kong.Close()
	rec := httptest.NewRecorder()
	appFor(t, Front, kong.URL).ServeHTTP(rec, post(chatPath, `{"model":"m","stream":true}`))
	want := ":live\n\n" + string(protocol.ErrorFrame(protocol.OpenAIChat, 0, "upstream stream interrupted"))
	if rec.Code != 200 || rec.Body.String() != want {
		t.Fatal(rec.Code, rec.Body.String())
	}
}

type recordSink chan Record

func (s recordSink) Enqueue(r Record) bool       { s <- r; return true }
func (s recordSink) Drain(context.Context) error { return nil }

func TestFrontManagedInferenceRoutes(t *testing.T) {
	const usage = `{"model":"served","usage":{"total_tokens":12}}`
	for _, tc := range []struct {
		path, media, reply string
		status             int
	}{
		{"completions", "application/json", usage, 200},
		{"embeddings", "application/json", usage, 200},
		{"rerank", "application/json", usage, 200},
		{"audio/transcriptions", "application/vendor+json; charset=utf-8", usage, 200},
		{"audio/transcriptions", "application/json", `{"text":"ok","usage":{"type":"duration","seconds":1}}`, 200},
		{"audio/transcriptions", "text/plain", "backend unavailable", 503},
	} {
		t.Run(tc.path+"/"+tc.media, func(t *testing.T) {
			path := "/workspace/ws/endpoint/ep/v1/" + tc.path
			kong := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != path || r.Header.Get(internal+"Unpacked") != "1" || r.Header.Get(internal+"Model") != "m" {
					t.Error(r.URL, r.Header)
				}
				raw, _ := io.ReadAll(r.Body)
				if bytes.Contains(raw, []byte("stream_options")) != (tc.path == "completions") {
					t.Errorf("unexpected stream options: %s", raw)
				}
				w.Header().Set(internal+"Consumer-Id", "consumer")
				w.Header().Set("Content-Type", tc.media)
				w.WriteHeader(tc.status)
				if tc.path == "embeddings" {
					io.WriteString(w, tc.reply[:1])
					w.(http.Flusher).Flush()
					time.Sleep(200 * time.Millisecond)
					io.WriteString(w, tc.reply[1:])
					return
				}
				io.WriteString(w, tc.reply)
			}))
			defer kong.Close()
			s := appFor(t, Front, kong.URL)
			s.Options.First = 100 * time.Millisecond
			s.Options.Idle = time.Second
			logs := make(recordSink, 1)
			s.Sink = logs
			r := post(path, `{"model":"m","stream":true}`)
			if tc.path == "audio/transcriptions" {
				var body bytes.Buffer
				m := multipart.NewWriter(&body)
				m.WriteField("model", "m")
				m.Close()
				r = httptest.NewRequest("POST", path, &body)
				r.Header.Set("Content-Type", m.FormDataContentType())
			}
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, r)
			if rec.Code != tc.status || rec.Body.String() != tc.reply {
				t.Fatal(rec.Code, rec.Body.String())
			}
			select {
			case log := <-logs:
				ai := log["ai"].(Record)
				trace := ai["trace"].(Record)
				if trace["response_body"] != tc.reply {
					t.Fatal(trace)
				}
				stats, billed := ai["statistics"].(Record)
				if billed != (tc.reply == usage) || billed && (stats["usage"].(*protocol.Usage).Total != 12 || trace["response_model"] != "served") {
					t.Fatal(ai)
				}
			default:
				t.Fatal("missing AI trace")
			}
		})
	}
}

func TestTraceUTF8Limit(t *testing.T) {
	for _, spill := range []int{0, 1, 2} {
		prefix := strings.Repeat("a", traceLimit-3+spill)
		raw := []byte(prefix + "界tail")
		x := exchange{RequestContext: RequestContext{Request: post("/", "")}, requestBody: raw[:traceLimit]}
		x.captureResponse(raw[:len(prefix)+1])
		x.captureResponse(raw[len(prefix)+1:])
		trace := x.record()["ai"].(Record)["trace"].(Record)
		want := prefix
		if spill == 0 {
			want += "界"
		}
		if trace["request_body"] != want || trace["response_body"] != want {
			t.Fatalf("split rune at %d", spill)
		}
	}
}
