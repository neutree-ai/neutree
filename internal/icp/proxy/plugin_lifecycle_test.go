package proxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neutree-ai/neutree/internal/icp/protocol"
	"github.com/neutree-ai/neutree/internal/icp/quota"
	"github.com/neutree-ai/neutree/internal/icp/state"
)

type traceRecorder struct {
	mu     sync.Mutex
	events []string
}

func (r *traceRecorder) add(ev string) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
}

func (r *traceRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]string(nil), r.events...)
}

type spyPlugin struct {
	name           string
	rec            *traceRecorder
	shortOnIngress *Response
	shortOnRequest *Response
}

func (p spyPlugin) Name() string {
	return p.name
}

func (p spyPlugin) Begin(_ *RequestContext) RequestHook {
	p.rec.add(p.name + ":begin")
	return &spyHook{p: p}
}

type spyHook struct {
	p spyPlugin
}

func (h *spyHook) OnIngress(_ *RequestContext) *Response {
	h.p.rec.add(h.p.name + ":ingress")
	return h.p.shortOnIngress
}

func (h *spyHook) OnRequest(_ *RequestContext) *Response {
	h.p.rec.add(h.p.name + ":request")
	return h.p.shortOnRequest
}

func (h *spyHook) OnComplete(_ *RequestContext) {
	h.p.rec.add(h.p.name + ":complete")
}

func (h *spyHook) OnAttemptStart(ac *AttemptContext) *Response {
	h.p.rec.add(h.p.name + ":attempt_start:" + ac.ID)
	return nil
}

func (h *spyHook) OnAttemptResponse(ac *AttemptContext) *Response {
	h.p.rec.add(h.p.name + ":attempt_response:" + ac.ID + ":" + string(ac.Disposition))
	return nil
}

func (h *spyHook) OnAttemptDone(ac *AttemptContext) {
	h.p.rec.add(h.p.name + ":attempt_done:" + ac.ID + ":" + string(ac.Disposition))
}

func TestShortCircuitLIFOCompletionAndConcurrencySlotRelease(t *testing.T) {
	routeKey := "/workspace/ws/endpoint/ep"
	limit := int64(100)
	snap := &state.ConfigSnapshot{
		Routes: map[string]state.Route{
			routeKey: {
				Key: routeKey, Workspace: "ws", Kind: "internal", Name: "ep", Managed: true,
				Upstreams: []state.Upstream{{ID: "endpoint:ws/ep", URL: "http://127.0.0.1:1", TargetProtocol: protocol.ProtocolOpenAIChat}},
			},
		},
		APIKeys:      map[string]state.APIKey{"k1": {ID: "k1", Secret: "sk-1", Workspace: "ws", TokenQuota: &limit}},
		Secrets:      map[string]string{"sk-1": "k1"},
		PolicyGroups: map[string]state.PolicyGroup{},
	}

	// 1. OnIngress short-circuit at plugin B: plugin C never enters (no Begin/OnIngress/OnComplete), B -> A complete in LIFO order.
	rec1 := &traceRecorder{}
	engine := state.NewEngine(snap, nil, 4)
	gwIngress, err := NewServer(Config{
		State: engine,
		Plugins: []Plugin{
			spyPlugin{name: "A", rec: rec1},
			spyPlugin{name: "B", rec: rec1, shortOnIngress: errorResponse(http.StatusForbidden, "custom", "denied", "ingress stop")},
			spyPlugin{name: "C", rec: rec1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	srv1 := httptest.NewServer(gwIngress)
	defer srv1.Close()

	resp1, err := http.Post(srv1.URL+routeKey+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	resp1.Body.Close()

	wantIngress := []string{"A:begin", "A:ingress", "B:begin", "B:ingress", "B:complete", "A:complete"}
	if got := rec1.snapshot(); resp1.StatusCode != http.StatusForbidden || !reflect.DeepEqual(got, wantIngress) {
		t.Fatalf("OnIngress short-circuit lifecycle mismatch: status=%d got=%v want=%v", resp1.StatusCode, got, wantIngress)
	}

	// 2. AccessPlugin acquires concurrency slot in OnIngress, then hits RateLimit 429 in OnIngress & Guardrail 400 in OnRequest:
	//    slot is released via LIFO OnComplete with zero leaked in-flight count and zero wasted Quota leases!
	leases := quota.NewLeasePool(10, time.Minute)
	engine2 := state.NewEngine(snap, leases, 4)
	access := NewAccessPlugin(AccessConfig{
		Concurrency: map[string]int{"k1": 1},
		RateLimits:  map[string]RateLimit{"k1": {Limit: 1, Window: time.Minute}},
	})
	rec2 := &traceRecorder{}
	gw2, err := NewServer(Config{
		State: engine2,
		Quota: leases,
		Plugins: []Plugin{
			spyPlugin{name: "outer", rec: rec2},
			access,
			NewGuardrailPlugin([]string{"forbidden-prompt"}, nil),
			spyPlugin{name: "post-guardrail", rec: rec2},
			NewQuotaPlugin(leases, nil, time.Minute),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	srv2 := httptest.NewServer(gw2)
	defer srv2.Close()

	// Request #1 passes rate limit (1/1) and hits Guardrail 400 in OnRequest (Anthropic error envelope); post-guardrail OnRequest & Quota Reserve never run!
	reqGuard, _ := http.NewRequest(http.MethodPost, srv2.URL+routeKey+"/anthropic/v1/messages", strings.NewReader(`{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"this has forbidden-prompt inside"}]}`))
	reqGuard.Header.Set("x-api-key", "sk-1")
	reqGuard.Header.Set("Content-Type", "application/json")
	respGuard, err := http.DefaultClient.Do(reqGuard)
	if err != nil {
		t.Fatal(err)
	}
	guardBody, _ := io.ReadAll(respGuard.Body)
	respGuard.Body.Close()

	if respGuard.StatusCode != http.StatusBadRequest || !strings.Contains(string(guardBody), `"type":"error"`) || !strings.Contains(string(guardBody), `"guardrail_blocked"`) {
		t.Fatalf("expected Anthropic 400 guardrail error, got status=%d body=%s", respGuard.StatusCode, guardBody)
	}
	if access.InFlight("k1") != 0 || len(leases.ActiveLeases()) != 0 || leases.Pools()[0].Used != 0 {
		t.Fatalf("leaked concurrency (%d) or quota (%+v) after Guardrail short-circuit", access.InFlight("k1"), leases.Pools())
	}
	for _, ev := range rec2.snapshot() {
		if ev == "post-guardrail:request" {
			t.Fatalf("downstream plugin OnRequest must not run after Guardrail short-circuit: %v", rec2.snapshot())
		}
	}

	// Request #2 exceeds fixed-window rate limit (2 > 1) after acquiring concurrency slot in OnIngress -> 429 + Retry-After, slot still returns to 0!
	reqRL, _ := http.NewRequest(http.MethodPost, srv2.URL+routeKey+"/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"clean"}]}`))
	reqRL.Header.Set("Authorization", "Bearer sk-1")
	reqRL.Header.Set("Content-Type", "application/json")
	respRL, err := http.DefaultClient.Do(reqRL)
	if err != nil {
		t.Fatal(err)
	}
	respRL.Body.Close()

	if respRL.StatusCode != http.StatusTooManyRequests || respRL.Header.Get("Retry-After") == "" || access.InFlight("k1") != 0 {
		t.Fatalf("rate-limit short-circuit failed to release concurrency slot: status=%d retryAfter=%q inflight=%d", respRL.StatusCode, respRL.Header.Get("Retry-After"), access.InFlight("k1"))
	}
}

func TestAttemptIsolationOnFailoverAndLIFOTelemetryObservation(t *testing.T) {
	primary503 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Even if failing primary emits a malformed X-Neutree-Usage header, failover must skip OnAttemptResponse and succeed on secondary!
		w.Header().Set("X-Neutree-Usage", "{corrupt-json")
		http.Error(w, `{"error":"overloaded"}`, http.StatusServiceUnavailable)
	}))
	defer primary503.Close()

	secondary200 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"model\":\"claude\",\"usage\":{\"input_tokens\":11}}}\n\n")
		fl.Flush()
		_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Recovered\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		_, _ = io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":7}}\n\n")
		_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		fl.Flush()
	}))
	defer secondary200.Close()

	routeKey := "/workspace/ws/external-endpoint/ee-iso"
	quotaLimit := int64(200)
	snap := &state.ConfigSnapshot{
		Routes: map[string]state.Route{
			routeKey: {
				Key: routeKey, Workspace: "ws", Kind: "external", Name: "ee-iso", Managed: true,
				Upstreams: []state.Upstream{
					{ID: "u-primary", URL: primary503.URL + "/v1", TargetProtocol: protocol.ProtocolOpenAIChat, TrustedUsage: true},
					{ID: "u-secondary", URL: secondary200.URL + "/v1", TargetProtocol: protocol.ProtocolAnthropicMessages},
				},
				ModelRoutes: map[string]state.ModelRoute{
					"pro": {
						MaxAttempts: 1,
						Targets: []state.Target{
							{Upstream: "u-primary", UpstreamModel: "gpt-4o", Priority: 1},
							{Upstream: "u-secondary", UpstreamModel: "claude", Priority: 2},
						},
					},
				},
			},
		},
		APIKeys:      map[string]state.APIKey{"k1": {ID: "k1", Secret: "sk-1", Workspace: "ws", TokenQuota: &quotaLimit}},
		Secrets:      map[string]string{"sk-1": "k1"},
		PolicyGroups: map[string]state.PolicyGroup{},
	}

	leases := quota.NewLeasePool(10, time.Minute)
	engine := state.NewEngine(snap, leases, 4)
	telemetry := NewTelemetryPlugin()
	rec := &traceRecorder{}

	gw, err := NewServer(Config{
		State: engine,
		Quota: leases,
		Plugins: []Plugin{
			telemetry,
			NewAccessPlugin(AccessConfig{}),
			spyPlugin{name: "spy", rec: rec},
			NewQuotaPlugin(leases, nil, time.Minute),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(gw)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+routeKey+"/v1/chat/completions", strings.NewReader(`{"model":"pro","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer sk-1")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(headerRequestID, "req-iso")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Recovered") {
		t.Fatalf("failover failed: status=%d body=%s", resp.StatusCode, body)
	}

	// Verify try-1 NEVER called OnAttemptResponse, while try-2 ran OnAttemptResponse + OnAttemptDone(deliver).
	gotEvents := rec.snapshot()
	for _, ev := range gotEvents {
		if strings.HasPrefix(ev, "spy:attempt_response:req-iso-try-1") {
			t.Fatalf("retrying attempt must NOT enter OnAttemptResponse: %v", gotEvents)
		}
	}

	// Verify LIFO completion: Telemetry (index 0) ran OnComplete AFTER Quota (index 3) settled rc.Usage (11+7=18 tokens)!
	ts := telemetry.Snapshot()
	if ts.Completed != 1 || ts.Retries != 1 || len(ts.RecentRequests) != 1 {
		t.Fatalf("unexpected telemetry snapshot: %+v", ts)
	}
	tr := ts.RecentRequests[0]
	if len(tr.Attempts) != 2 || tr.Attempts[0].Disposition != AttemptRetry || tr.Attempts[0].Status != 503 || tr.Attempts[1].Disposition != AttemptDeliver || tr.Attempts[1].Status != 200 {
		t.Fatalf("unexpected attempt traces: %+v", tr.Attempts)
	}
	if tr.Usage == nil || quota.BillableUnits(tr.Usage.Measures) != 18 || tr.TTFT <= 0 {
		t.Fatalf("TelemetryPlugin did not observe settled Usage (18) or TTFT in LIFO OnComplete: %+v", tr)
	}
}

func TestModelListDynamicFilteringSyntheticAndUpstreamWithContentLength(t *testing.T) {
	var extUpstreamCalls, intUpstreamCalls atomic.Int32

	extUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		extUpstreamCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer extUpstream.Close()

	intUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		intUpstreamCalls.Add(1)
		if r.Header.Get("Accept-Encoding") == "br" {
			t.Errorf("upstream received raw client Accept-Encoding: br, which would break JSON model filtering")
		}
		// Return gzip-compressed OpenAI model list when client/transport accepts gzip to verify transparent gunzip + Content-Length rewrite.
		rawJSON := `{"object":"list","data":[{"id":"qwen-72b","object":"model"},{"id":"deepseek-r1","object":"model"},{"id":"bge-m3","object":"model"}]}`
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("ETag", `"stale-etag"`)
			gz := gzip.NewWriter(w)
			_, _ = gz.Write([]byte(rawJSON))
			_ = gz.Close()

			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, rawJSON)
	}))
	defer intUpstream.Close()

	extRoute := "/workspace/ws/external-endpoint/ee-models"
	intRoute := "/workspace/ws/endpoint/ie-models"
	tinyQuota := int64(5)
	snap := &state.ConfigSnapshot{
		Routes: map[string]state.Route{
			extRoute: {
				Key: extRoute, Workspace: "ws", Kind: "external", Name: "ee-models", Managed: true,
				Upstreams: []state.Upstream{{ID: "u-ext", URL: extUpstream.URL, TargetProtocol: protocol.ProtocolOpenAIChat}},
				ModelRoutes: map[string]state.ModelRoute{
					"alpha": {},
					"beta":  {},
					"gamma": {},
				},
			},
			intRoute: {
				Key: intRoute, Workspace: "ws", Kind: "internal", Name: "ie-models", Managed: true,
				Upstreams: []state.Upstream{{ID: "endpoint:ws/ie-models", URL: intUpstream.URL, TargetProtocol: protocol.ProtocolOpenAIChat}},
			},
		},
		PolicyGroups: map[string]state.PolicyGroup{
			"g-deny-beta": {
				ID:           "g-deny-beta",
				DeniedModels: []state.GroupModelRule{{Model: "beta", Workspace: "ws"}, {Model: "deepseek-r1", Workspace: "ws"}},
			},
		},
		APIKeys: map[string]state.APIKey{
			"k-disabled": {ID: "k-disabled", Secret: "sk-disabled", Workspace: "ws", Disabled: true},
			"k-scoped": {
				ID: "k-scoped", Secret: "sk-scoped", Workspace: "ws",
				PolicyGroupIDs: []string{"g-deny-beta"},
				TokenQuota:     &tinyQuota,
				AllowedModels: []state.AllowedModel{
					{Model: "alpha", Type: "external", EndpointName: "ee-models"},
					{Model: "beta", Type: "external", EndpointName: "ee-models"},
					{Model: "qwen-72b", Type: "internal", EndpointName: "ie-models"},
					{Model: "deepseek-r1", Type: "internal", EndpointName: "ie-models"},
				},
			},
		},
		Secrets: map[string]string{"sk-disabled": "k-disabled", "sk-scoped": "k-scoped"},
	}

	leases := quota.NewLeasePool(5, time.Minute)
	engine := state.NewEngine(snap, leases, 4)
	gw, err := NewServer(Config{State: engine, Quota: leases})
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(gw)
	defer srv.Close()

	// 1. Disabled key on synthetic External Endpoint /v1/models is rejected 403 key_disabled in OnIngress (no Kong header_filter workaround needed!).
	reqDis, _ := http.NewRequest(http.MethodGet, srv.URL+extRoute+"/v1/models", nil)
	reqDis.Header.Set("Authorization", "Bearer sk-disabled")
	respDis, err := http.DefaultClient.Do(reqDis)
	if err != nil {
		t.Fatal(err)
	}
	disBody, _ := io.ReadAll(respDis.Body)
	respDis.Body.Close()

	if respDis.StatusCode != http.StatusForbidden || !strings.Contains(string(disBody), "key_disabled") {
		t.Fatalf("expected 403 key_disabled on disabled key, got %d: %s", respDis.StatusCode, disBody)
	}

	// 2. External Endpoint Anthropic /anthropic/v1/models synthesizes [alpha, beta, gamma], filters via Key AllowedModels + PolicyGroup DeniedModels -> [alpha], coherent first_id/last_id, 0 upstream calls, 0 quota!
	reqAnt, _ := http.NewRequest(http.MethodGet, srv.URL+extRoute+"/anthropic/v1/models", nil)
	reqAnt.Header.Set("x-api-key", "sk-scoped")
	respAnt, err := http.DefaultClient.Do(reqAnt)
	if err != nil {
		t.Fatal(err)
	}
	antRaw, _ := io.ReadAll(respAnt.Body)
	respAnt.Body.Close()

	var antList struct {
		Data    []map[string]any `json:"data"`
		HasMore bool             `json:"has_more"`
		FirstID string           `json:"first_id"`
		LastID  string           `json:"last_id"`
	}
	if err = json.Unmarshal(antRaw, &antList); err != nil {
		t.Fatal(err)
	}
	if len(antList.Data) != 1 || antList.Data[0]["id"] != "alpha" || antList.FirstID != "alpha" || antList.LastID != "alpha" {
		t.Fatalf("unexpected filtered Anthropic model list: %s", antRaw)
	}
	if respAnt.Header.Get("Content-Length") != strconv.Itoa(len(antRaw)) || extUpstreamCalls.Load() != 0 || leases.Pools()[0].Used != 0 {
		t.Fatalf("synthetic model list Content-Length/upstream/quota mismatch: cl=%q len=%d calls=%d pools=%+v", respAnt.Header.Get("Content-Length"), len(antRaw), extUpstreamCalls.Load(), leases.Pools())
	}

	// 3. Internal Endpoint /v1/models forwards upstream (stripping client Accept-Encoding: br), filters [qwen-72b, deepseek-r1, bge-m3] -> [qwen-72b] without polluting Anthropic fields, exact Content-Length!
	tr := &http.Transport{DisableCompression: true}
	defer tr.CloseIdleConnections()
	rawClient := &http.Client{Transport: tr}

	reqInt, _ := http.NewRequest(http.MethodGet, srv.URL+intRoute+"/v1/models", nil)
	reqInt.Header.Set("Authorization", "Bearer sk-scoped")
	reqInt.Header.Set("Accept-Encoding", "br")
	respInt, err := rawClient.Do(reqInt)
	if err != nil {
		t.Fatal(err)
	}
	intRaw, _ := io.ReadAll(respInt.Body)
	respInt.Body.Close()

	if intUpstreamCalls.Load() != 1 || respInt.Header.Get("ETag") != "" || respInt.Header.Get("Content-Length") != strconv.Itoa(len(intRaw)) {
		t.Fatalf("internal model list headers mismatch: calls=%d etag=%q cl=%q len=%d body=%s", intUpstreamCalls.Load(), respInt.Header.Get("ETag"), respInt.Header.Get("Content-Length"), len(intRaw), intRaw)
	}
	if strings.Contains(string(intRaw), "first_id") || strings.Contains(string(intRaw), "deepseek-r1") || strings.Contains(string(intRaw), "bge-m3") || !strings.Contains(string(intRaw), `"qwen-72b"`) {
		t.Fatalf("internal OpenAI model list filtering corrupted payload: %s", intRaw)
	}
}

func TestGuardrailInputRedactOutputRedactAndMidStreamCutoff(t *testing.T) {
	var receivedPrompt atomic.Value
	upstreamAborted := make(chan struct{})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		b, _ := json.Marshal(payload)
		receivedPrompt.Store(string(b))

		stream, _ := payload["stream"].(bool)
		if !stream {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"c1","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"Token sk-live-secret-999 ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":4}}`)

			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		chunks := []string{"Hello sk-live-secret-999! ", "Now leaking TOP_SECRET_KEY", "never-sent"}
		for _, ch := range chunks {
			select {
			case <-r.Context().Done():
				select {
				case <-upstreamAborted:
				default:
					close(upstreamAborted)
				}

				return
			default:
			}
			_, _ = io.WriteString(w, `data: {"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"`+ch+`"}}]}`+"\n\n")
			fl.Flush()
			time.Sleep(15 * time.Millisecond)
		}
	}))
	defer upstream.Close()

	routeKey := "/workspace/ws/endpoint/ep-gr"
	limit := int64(200)
	snap := &state.ConfigSnapshot{
		Routes: map[string]state.Route{
			routeKey: {
				Key: routeKey, Workspace: "ws", Kind: "internal", Name: "ep-gr", Managed: true,
				Upstreams: []state.Upstream{{ID: "endpoint:ws/ep-gr", URL: upstream.URL + "/v1", TargetProtocol: protocol.ProtocolOpenAIChat}},
			},
		},
		APIKeys:      map[string]state.APIKey{"k1": {ID: "k1", Secret: "sk-1", Workspace: "ws", TokenQuota: &limit}},
		Secrets:      map[string]string{"sk-1": "k1"},
		PolicyGroups: map[string]state.PolicyGroup{},
	}

	leases := quota.NewLeasePool(10, time.Minute)
	engine := state.NewEngine(snap, leases, 4)
	gw, err := NewServer(Config{
		State: engine,
		Quota: leases,
		Plugins: []Plugin{
			NewTelemetryPlugin(),
			NewAccessPlugin(AccessConfig{}),
			NewGuardrailPlugin([]string{"TOP_SECRET_KEY"}, []string{"sk-live-secret-999"}),
			NewQuotaPlugin(leases, nil, time.Minute),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(gw)
	defer srv.Close()

	// 1. Non-streaming: both prompt and response redact "sk-live-secret-999" -> "[REDACTED]".
	req1, _ := http.NewRequest(http.MethodPost, srv.URL+routeKey+"/v1/chat/completions", strings.NewReader(`{"model":"m","stream":false,"messages":[{"role":"user","content":"My key is sk-live-secret-999"}]}`))
	req1.Header.Set("Authorization", "Bearer sk-1")
	req1.Header.Set("Content-Type", "application/json")
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatal(err)
	}
	body1, _ := io.ReadAll(resp1.Body)
	resp1.Body.Close()

	upSeen, _ := receivedPrompt.Load().(string)
	if resp1.StatusCode != http.StatusOK || strings.Contains(upSeen, "sk-live-secret-999") || !strings.Contains(upSeen, "[REDACTED]") {
		t.Fatalf("input redaction did not reach upstream: status=%d upSeen=%s", resp1.StatusCode, upSeen)
	}
	if strings.Contains(string(body1), "sk-live-secret-999") || !strings.Contains(string(body1), "Token [REDACTED] ok") {
		t.Fatalf("non-streaming output redaction failed: %s", body1)
	}

	// 2. Streaming Anthropic client -> OpenAI upstream: chunk 1 is live-redacted, chunk 2 hits TOP_SECRET_KEY -> cancels upstream & emits native Anthropic SSE error!
	req2, _ := http.NewRequest(http.MethodPost, srv.URL+routeKey+"/anthropic/v1/messages", strings.NewReader(`{"model":"m","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"go"}]}`))
	req2.Header.Set("x-api-key", "sk-1")
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	sseOut, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()

	select {
	case <-upstreamAborted:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream context was not cancelled on mid-stream guardrail block")
	}

	outStr := string(sseOut)
	if !strings.Contains(outStr, "Hello [REDACTED]!") || strings.Contains(outStr, "TOP_SECRET_KEY") || !strings.Contains(outStr, "event: error") {
		t.Fatalf("streaming guardrail redaction/cutoff mismatch:\n%s", outStr)
	}
	if len(leases.ActiveLeases()) != 0 {
		t.Fatalf("leaked active leases after guardrail stream cutoff: %+v", leases.ActiveLeases())
	}

	// 3. Aborted non-streaming response (blocked in OnCanonicalResponse) marks AttemptAbort and tombstones attempt against late async usage callbacks!
	abortLeases := quota.NewLeasePool(10, time.Minute)
	abortEngine := state.NewEngine(snap, abortLeases, 4)
	abortTel := NewTelemetryPlugin()
	abortGW, err := NewServer(Config{
		State: abortEngine,
		Quota: abortLeases,
		Plugins: []Plugin{
			abortTel,
			NewAccessPlugin(AccessConfig{}),
			NewGuardrailPlugin([]string{"Token"}, nil),
			NewQuotaPlugin(abortLeases, []byte("sec"), time.Minute),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	abortSrv := httptest.NewServer(abortGW)
	defer abortSrv.Close()

	req3, _ := http.NewRequest(http.MethodPost, abortSrv.URL+routeKey+"/v1/chat/completions", strings.NewReader(`{"model":"m","stream":false,"messages":[{"role":"user","content":"clean"}]}`))
	req3.Header.Set("Authorization", "Bearer sk-1")
	req3.Header.Set("Content-Type", "application/json")
	req3.Header.Set(headerRequestID, "req-abort-1")
	resp3, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	resp3.Body.Close()

	if resp3.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 on non-streaming output guardrail block, got %d", resp3.StatusCode)
	}
	// Late callback for aborted attempt must NOT charge client pool!
	if _, err = abortLeases.Correct(quota.UsageRecord{
		RequestID: "req-abort-1", AttemptID: "req-abort-1-try-1", APIKeyID: "k1",
		PoolIDs:  []string{quota.KeyPoolID("k1")},
		Measures: []quota.Measure{{Metric: "input_tokens", Unit: "tokens", Quantity: 50}},
	}); err != nil {
		t.Fatal(err)
	}
	if abortLeases.Pools()[0].Used != 0 || abortTel.Snapshot().RecentRequests[0].Attempts[0].Disposition != AttemptAbort {
		t.Fatalf("aborted attempt leaked late callback charge or wrong disposition: pools=%+v tel=%+v", abortLeases.Pools(), abortTel.Snapshot().RecentRequests[0])
	}
}

func TestClientDisconnectAndDisableCompressionModelListGunzip(t *testing.T) {
	firstChunkReady := make(chan struct{})
	upstreamDisconnected := make(chan struct{})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/v1/models") {
			var gzBuf bytes.Buffer
			gz := gzip.NewWriter(&gzBuf)
			_, _ = gz.Write([]byte(`{"object":"list","data":[{"id":"allowed-m","object":"model"},{"id":"hidden-m","object":"model"}]}`))
			_ = gz.Close()

			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = w.Write(gzBuf.Bytes())

			return
		}
		if strings.HasSuffix(r.URL.Path, "/v1/health") {
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"ok":true}`)

			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"id\":\"c1\",\"model\":\"allowed-m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello \"}}]}\n\n")
		fl.Flush()
		close(firstChunkReady)
		<-r.Context().Done()
		close(upstreamDisconnected)
	}))
	defer upstream.Close()

	routeKey := "/workspace/ws/endpoint/ep-single"
	limit := int64(100)
	upID := "endpoint:ws/ep-single"
	snap := &state.ConfigSnapshot{
		Routes: map[string]state.Route{
			routeKey: {
				Key: routeKey, Workspace: "ws", Kind: "internal", Name: "ep-single", Managed: true,
				Upstreams:   []state.Upstream{{ID: upID, URL: upstream.URL, TargetProtocol: protocol.ProtocolOpenAIChat}},
				ModelRoutes: map[string]state.ModelRoute{"allowed-m": {Targets: []state.Target{{Upstream: upID, UpstreamModel: "allowed-m"}}}},
			},
		},
		APIKeys: map[string]state.APIKey{
			"k1": {
				ID: "k1", Secret: "sk-1", Workspace: "ws", TokenQuota: &limit,
				AllowedModels: []state.AllowedModel{{Model: "allowed-m"}},
			},
		},
		Secrets:      map[string]string{"sk-1": "k1"},
		PolicyGroups: map[string]state.PolicyGroup{},
	}

	leases := quota.NewLeasePool(10, time.Minute)
	engine := state.NewEngine(snap, leases, 4)
	telemetry := NewTelemetryPlugin()
	access := NewAccessPlugin(AccessConfig{Concurrency: map[string]int{"k1": 2}})

	// Custom upstream client with DisableCompression: true verifies AccessPlugin gunzips /v1/models explicitly!
	noDecompTransport := &http.Transport{DisableCompression: true}
	defer noDecompTransport.CloseIdleConnections()

	gw, err := NewServer(Config{
		State:  engine,
		Quota:  leases,
		Client: &http.Client{Transport: noDecompTransport},
		Plugins: []Plugin{
			telemetry,
			access,
			NewQuotaPlugin(leases, nil, time.Minute),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(gw)
	defer srv.Close()

	// 1. DisableCompression=true upstream gzip /v1/models is gunzipped, filtered, and returned as valid plain JSON!
	reqModels, _ := http.NewRequest(http.MethodGet, srv.URL+routeKey+"/v1/models", nil)
	reqModels.Header.Set("Authorization", "Bearer sk-1")
	respModels, err := http.DefaultClient.Do(reqModels)
	if err != nil {
		t.Fatal(err)
	}
	modelsBody, _ := io.ReadAll(respModels.Body)
	respModels.Body.Close()

	if respModels.Header.Get("Content-Encoding") != "" || !strings.Contains(string(modelsBody), `"allowed-m"`) || strings.Contains(string(modelsBody), `"hidden-m"`) {
		t.Fatalf("DisableCompression gzip /v1/models filter failed: ce=%q body=%q", respModels.Header.Get("Content-Encoding"), modelsBody)
	}

	// 2. Body-less GET with Content-Type: application/json on single-model route resolves singleRouteModel without 403!
	reqGetJSON, _ := http.NewRequest(http.MethodGet, srv.URL+routeKey+"/v1/health", nil)
	reqGetJSON.Header.Set("Authorization", "Bearer sk-1")
	reqGetJSON.Header.Set("Content-Type", "application/json")
	respGetJSON, err := http.DefaultClient.Do(reqGetJSON)
	if err != nil {
		t.Fatal(err)
	}
	respGetJSON.Body.Close()
	if respGetJSON.StatusCode != http.StatusOK {
		t.Fatalf("GET with Content-Type: application/json on single-model route failed: got %d", respGetJSON.StatusCode)
	}

	// 3. Client active mid-stream disconnect cancels upstream and releases concurrency slot, upstream InFlight, and quota lease!
	clientCtx, cancelClient := context.WithCancel(context.Background())
	reqStream, _ := http.NewRequestWithContext(clientCtx, http.MethodPost, srv.URL+routeKey+"/v1/chat/completions", strings.NewReader(`{"model":"allowed-m","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	reqStream.Header.Set("Authorization", "Bearer sk-1")
	reqStream.Header.Set("Content-Type", "application/json")

	respStream, err := http.DefaultClient.Do(reqStream)
	if err != nil {
		t.Fatal(err)
	}

	<-firstChunkReady
	var small [16]byte
	_, _ = respStream.Body.Read(small[:])
	cancelClient()
	_ = respStream.Body.Close()

	select {
	case <-upstreamDisconnected:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream was not cancelled on client disconnect")
	}

	// Wait briefly for server handler goroutine defer LIFO stack to finish after client socket closes.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if access.InFlight("k1") == 0 && engine.Runtime(upID).InFlight == 0 && len(leases.ActiveLeases()) == 0 && telemetry.Snapshot().Completed == 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if access.InFlight("k1") != 0 || engine.Runtime(upID).InFlight != 0 || len(leases.ActiveLeases()) != 0 || telemetry.Snapshot().Completed != 3 {
		t.Fatalf("leaked resources after client disconnect: keyInflight=%d upInflight=%d activeLeases=%+v telCompleted=%d",
			access.InFlight("k1"), engine.Runtime(upID).InFlight, leases.ActiveLeases(), telemetry.Snapshot().Completed)
	}
}
