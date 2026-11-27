package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neutree-ai/neutree/internal/icp/protocol"
	"github.com/neutree-ai/neutree/internal/icp/quota"
	"github.com/neutree-ai/neutree/internal/icp/state"
)

func TestLegacyFallbackStreamsMultipartZeroBuffered(t *testing.T) {
	firstChunkSeen := make(chan struct{})
	secondChunkGate := make(chan struct{})
	var gotPath, gotAuth, gotCT, gotBody string

	legacyKong := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")

		var buf [64]byte
		n, err := r.Body.Read(buf[:])
		if err != nil && err != io.EOF {
			t.Errorf("read first chunk: %v", err)
		}
		firstChunk := string(buf[:n])
		close(firstChunkSeen)

		rest, _ := io.ReadAll(r.Body)
		gotBody = firstChunk + string(rest)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer legacyKong.Close()

	engine := state.NewEngine(&state.ConfigSnapshot{
		LegacyFallbackEnabled: true,
		ManagedRoutes:         map[string]bool{},
		Routes: map[string]state.Route{
			"/workspace/ws/endpoint/unmanaged": {Key: "/workspace/ws/endpoint/unmanaged", Workspace: "ws", Managed: true},
		},
		APIKeys:      map[string]state.APIKey{},
		Secrets:      map[string]string{},
		PolicyGroups: map[string]state.PolicyGroup{},
	}, nil, 4)

	gw, err := NewServer(Config{State: engine, Quota: quota.NewLeasePool(10, time.Minute), LegacyKongURL: legacyKong.URL})
	if err != nil {
		t.Fatal(err)
	}
	gwSrv := httptest.NewServer(gw)
	defer gwSrv.Close()

	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write([]byte("--bound\r\nContent-Disposition: form-data; name=\"file\"\r\n\r\nchunk-1:"))
		<-firstChunkSeen // Proves gateway forwarded chunk-1 BEFORE client even wrote chunk-2 (zero whole-body buffering!)
		close(secondChunkGate)
		_, _ = pw.Write([]byte("chunk-2\r\n--bound--\r\n"))
		_ = pw.Close()
	}()

	req, _ := http.NewRequest(http.MethodPost, gwSrv.URL+"/workspace/ws/endpoint/unmanaged/v1/audio/transcriptions?lang=en", pr)
	req.Header.Set("Authorization", "Bearer raw-kong-token")
	req.Header.Set("Content-Type", "multipart/form-data; boundary=bound")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	<-secondChunkGate

	if resp.StatusCode != http.StatusAccepted || gotPath != "/workspace/ws/endpoint/unmanaged/v1/audio/transcriptions?lang=en" || gotAuth != "Bearer raw-kong-token" || !strings.Contains(gotCT, "boundary=bound") || !strings.Contains(gotBody, "chunk-1:chunk-2") {
		t.Fatalf("legacy fallback corrupted stream: status=%d path=%q auth=%q ct=%q body=%q", resp.StatusCode, gotPath, gotAuth, gotCT, gotBody)
	}
}

func TestPassthroughSaltsPrefixAndStripsHeaderAndSSEUsage(t *testing.T) {
	var seenPrefix, seenToken string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPrefix = r.Header.Get(headerPrefixHash)
		seenToken = r.Header.Get(headerUsageToken)
		if r.Header.Get("Authorization") != "Bearer up-secret" {
			t.Errorf("unexpected upstream auth: %q", r.Header.Get("Authorization"))
		}

		if r.URL.Path == "/ws/ep/v1/audio/transcriptions" {
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("X-Neutree-Usage", `{"measures":[{"metric":"audio_seconds","unit":"seconds","quantity":45}]}`)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"text":"hello audio"}`))

			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "event: custom\ndata: {\"step\":1}\n\n")
		fl.Flush()
		_, _ = io.WriteString(w, "event: neutree.usage\ndata: {\"measures\":[{\"metric\":\"ocr_pages\",\"unit\":\"pages\",\"quantity\":6}]}\n\n")
		fl.Flush()
	}))
	defer upstream.Close()

	routeKey := "/workspace/ws/endpoint/ep"
	snap := &state.ConfigSnapshot{
		Routes: map[string]state.Route{
			routeKey: {
				Key: routeKey, Workspace: "ws", Kind: "internal", Name: "ep", Managed: true,
				Upstreams: []state.Upstream{{ID: "endpoint:ws/ep", URL: upstream.URL + "/ws/ep", TargetProtocol: protocol.ProtocolOpenAIChat, AuthType: "bearer", Credential: "up-secret"}},
			},
		},
		APIKeys:      map[string]state.APIKey{"k1": {ID: "k1", Secret: "sk-1", Workspace: "ws"}},
		Secrets:      map[string]string{"sk-1": "k1"},
		PolicyGroups: map[string]state.PolicyGroup{},
	}
	leases := quota.NewLeasePool(10, time.Minute)
	engine := state.NewEngine(snap, leases, 4)

	gw, err := NewServer(Config{State: engine, Quota: leases, CallbackSecret: []byte("hmac-key")})
	if err != nil {
		t.Fatal(err)
	}
	gwSrv := httptest.NewServer(gw)
	defer gwSrv.Close()

	// 1. Non-SSE multipart passthrough with X-Neutree-Usage header stripping & tenant prefix salting.
	req1, _ := http.NewRequest(http.MethodPost, gwSrv.URL+routeKey+"/v1/audio/transcriptions", strings.NewReader("--b\r\naudio-bytes\r\n--b--"))
	req1.Header.Set("x-api-key", "sk-1")
	req1.Header.Set("Content-Type", "multipart/form-data; boundary=b")
	req1.Header.Set(headerPrefixHash, "client-raw-hash")
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatal(err)
	}
	body1, _ := io.ReadAll(resp1.Body)
	resp1.Body.Close()

	if resp1.Header.Get("X-Neutree-Usage") != "" || seenPrefix != "ws:"+routeKey+":client-raw-hash" || seenToken == "" || !strings.Contains(string(body1), "hello audio") {
		t.Fatalf("passthrough header check failed: usageHdr=%q prefix=%q token=%q body=%q", resp1.Header.Get("X-Neutree-Usage"), seenPrefix, seenToken, body1)
	}

	// 2. SSE passthrough stripping event: neutree.usage tail frame.
	req2, _ := http.NewRequest(http.MethodPost, gwSrv.URL+routeKey+"/v1/custom-stream", strings.NewReader("raw"))
	req2.Header.Set("Authorization", "Bearer sk-1")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()

	if strings.Contains(string(body2), "neutree.usage") || !strings.Contains(string(body2), `{"step":1}`) {
		t.Fatalf("SSE tail usage frame was not cleanly stripped: %q", body2)
	}

	bills := leases.Bills()
	if len(bills) != 2 {
		t.Fatalf("expected 2 usage bills, got %+v", bills)
	}
}

func TestHeterogeneousFailoverResponsesClientChat429ToAnthropicStream(t *testing.T) {
	var chatHits, antHits atomic.Int32

	chatUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chatHits.Add(1)
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer openai-key" || r.Header.Get(headerPrefixHash) == "" {
			t.Errorf("unexpected chat attempt: path=%s auth=%q prefix=%q", r.URL.Path, r.Header.Get("Authorization"), r.Header.Get(headerPrefixHash))
		}
		http.Error(w, `{"error":"rate limited"}`, http.StatusTooManyRequests)
	}))
	defer chatUpstream.Close()

	antUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		antHits.Add(1)
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "ant-key" || r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("unexpected anthropic attempt: path=%s x-api-key=%q ver=%q", r.URL.Path, r.Header.Get("x-api-key"), r.Header.Get("anthropic-version"))
		}
		if r.Header.Get(headerUsageToken) != "" {
			t.Errorf("untrusted external upstream should not receive forged or internal usage token")
		}
		var antReq map[string]any
		_ = json.NewDecoder(r.Body).Decode(&antReq)
		if antReq["model"] != "claude-sonnet-4" {
			t.Errorf("expected recompiled model claude-sonnet-4, got %v", antReq["model"])
		}

		// Untrusted external upstream attempts to spoof zero usage via X-Neutree-Usage; gateway must strip & ignore it!
		w.Header().Set("X-Neutree-Usage", `{"measures":[{"metric":"quota.credits","unit":"credits","quantity":0}]}`)
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_77\",\"model\":\"claude-sonnet-4\",\"usage\":{\"input_tokens\":25}}}\n\n")
		fl.Flush()
		_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Failover works!\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		_, _ = io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n")
		_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		fl.Flush()
	}))
	defer antUpstream.Close()

	routeKey := "/workspace/ws/external-endpoint/ee"
	limit := int64(500)
	snap := &state.ConfigSnapshot{
		Routes: map[string]state.Route{
			routeKey: {
				Key: routeKey, Workspace: "ws", Kind: "external", Name: "ee", Managed: true,
				Upstreams: []state.Upstream{
					{ID: "ee#chat", URL: chatUpstream.URL + "/v1", TargetProtocol: protocol.ProtocolOpenAIChat, AuthType: "bearer", Credential: "openai-key"},
					{ID: "ee#ant", URL: antUpstream.URL + "/v1", TargetProtocol: protocol.ProtocolAnthropicMessages, AuthType: "api_key", Credential: "ant-key"},
				},
				ModelRoutes: map[string]state.ModelRoute{
					"unified-pro": {
						MaxAttempts: 1,
						Targets: []state.Target{
							{Upstream: "ee#chat", UpstreamModel: "gpt-4o", Priority: 1},
							{Upstream: "ee#ant", UpstreamModel: "claude-sonnet-4", Priority: 2},
						},
					},
				},
			},
		},
		APIKeys:      map[string]state.APIKey{"k1": {ID: "k1", Secret: "sk-1", Workspace: "ws", TokenQuota: &limit}},
		Secrets:      map[string]string{"sk-1": "k1"},
		PolicyGroups: map[string]state.PolicyGroup{},
	}
	leases := quota.NewLeasePool(10, time.Minute)
	engine := state.NewEngine(snap, leases, 4)

	gw, err := NewServer(Config{State: engine, Quota: leases})
	if err != nil {
		t.Fatal(err)
	}
	gwSrv := httptest.NewServer(gw)
	defer gwSrv.Close()

	req, _ := http.NewRequest(http.MethodPost, gwSrv.URL+routeKey+"/v1/responses", strings.NewReader(`{"model":"unified-pro","stream":true,"instructions":"Sys","input":"Hello"}`))
	req.Header.Set("Authorization", "Bearer sk-1")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(headerRequestID, "req-failover-1")
	req.Header.Set(headerUsageToken, "client-forged-token")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	rawSSE, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK || resp.Header.Get("X-Neutree-Usage") != "" || chatHits.Load() != 1 || antHits.Load() != 1 {
		t.Fatalf("unexpected failover counts: status=%d chat=%d ant=%d body=%s", resp.StatusCode, chatHits.Load(), antHits.Load(), rawSSE)
	}
	if !strings.Contains(string(rawSSE), "event: response.output_text.delta") || !strings.Contains(string(rawSSE), "Failover works!") || !strings.Contains(string(rawSSE), "event: response.completed") {
		t.Fatalf("expected translated OpenAI Responses SSE, got:\n%s", rawSSE)
	}

	// Both attempts (failed try-1 and winning try-2) are recorded in ledger, while client quota pool is charged only once (30)!
	bills := leases.Bills()
	if len(bills) != 2 {
		t.Fatalf("expected 2 attempt bills (try-1 failed + try-2 settled), got %+v", bills)
	}
	// Late callback on failed try-1 must NOT charge client quota pool!
	if _, err := leases.Correct(quota.UsageRecord{
		RequestID: "req-failover-1", AttemptID: "req-failover-1-try-1", APIKeyID: "k1",
		PoolIDs:  []string{quota.KeyPoolID("k1")},
		Measures: []quota.Measure{{Metric: "input_tokens", Unit: "tokens", Quantity: 99}},
	}); err != nil {
		t.Fatal(err)
	}
	pools := leases.Pools()
	if len(pools) != 1 || pools[0].Used != 30 {
		t.Fatalf("expected client pool Used=30 (untrusted zero-header & failed try-1 callback ignored), got %+v", pools)
	}
}

func TestMidStreamQuotaExhaustionCancelsUpstreamAndEmitsStreamError(t *testing.T) {
	upstreamCancelled := make(chan struct{})

	chatStreamUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for i := 0; i < 20; i++ {
			select {
			case <-r.Context().Done():
				close(upstreamCancelled)
				return
			default:
			}
			_, _ = io.WriteString(w, "data: {\"id\":\"c1\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"0123456789abcdef\"}}]}\n\n")
			fl.Flush()
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer chatStreamUpstream.Close()

	routeKey := "/workspace/ws/endpoint/ep"
	tightLimit := int64(6) // 16 chars = 4 units per chunk -> chunk 1 ok (5<=6), chunk 2 exceeds 6!
	snap := &state.ConfigSnapshot{
		Routes: map[string]state.Route{
			routeKey: {
				Key: routeKey, Workspace: "ws", Kind: "internal", Name: "ep", Managed: true,
				Upstreams: []state.Upstream{{ID: "endpoint:ws/ep", URL: chatStreamUpstream.URL, TargetProtocol: protocol.ProtocolOpenAIChat}},
			},
		},
		APIKeys:      map[string]state.APIKey{"k1": {ID: "k1", Secret: "sk-1", Workspace: "ws", TokenQuota: &tightLimit}},
		Secrets:      map[string]string{"sk-1": "k1"},
		PolicyGroups: map[string]state.PolicyGroup{},
	}
	leases := quota.NewLeasePool(2, time.Minute)
	engine := state.NewEngine(snap, leases, 4)

	gw, err := NewServer(Config{State: engine, Quota: leases})
	if err != nil {
		t.Fatal(err)
	}
	gwSrv := httptest.NewServer(gw)
	defer gwSrv.Close()

	req, _ := http.NewRequest(http.MethodPost, gwSrv.URL+routeKey+"/anthropic/v1/messages", strings.NewReader(`{"model":"m","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"long story"}]}`))
	req.Header.Set("x-api-key", "sk-1")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	select {
	case <-upstreamCancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream request context was not cancelled upon mid-stream quota exhaustion")
	}

	if !strings.Contains(string(out), "event: error") || !strings.Contains(string(out), "quota exceeded") {
		t.Fatalf("expected native Anthropic SSE error event on quota cutoff, got:\n%s", out)
	}
	if len(leases.ActiveLeases()) != 0 {
		t.Fatalf("leaked active leases after stream cutoff: %+v", leases.ActiveLeases())
	}
	if pools := leases.Pools(); len(pools) != 1 || pools[0].Used != 4 || pools[0].Used > tightLimit {
		t.Fatalf("expected settled pool usage 4 (<= tightLimit %d) without counting rejected chunk, got %+v", tightLimit, pools)
	}
}

func TestRuntimeHealthLoadAndPrefixAffinitySelection(t *testing.T) {
	rt := state.NewRuntimeTable()
	unhealthy := false
	rt.Update(state.RuntimeEvent{UpstreamID: "u-down", Healthy: &unhealthy, PrefixHashes: []string{"h-match"}})
	rt.Update(state.RuntimeEvent{UpstreamID: "u-busy", QueueDepth: 10})
	rt.Update(state.RuntimeEvent{UpstreamID: "u-affinity", QueueDepth: 5, PrefixHashes: []string{"h-match"}})
	rt.Update(state.RuntimeEvent{UpstreamID: "u-idle", QueueDepth: 0})

	route := state.Route{
		Upstreams: []state.Upstream{{ID: "u-down"}, {ID: "u-busy"}, {ID: "u-idle"}, {ID: "u-affinity"}},
	}

	cands, ok := chooseCandidates(rt, route, "m", "h-match")
	if !ok || len(cands) != 4 {
		t.Fatalf("unexpected candidates: %+v", cands)
	}
	// Healthy + PrefixHash hit ("u-affinity") beats lower-queue non-affinity ("u-idle"), and unhealthy ("u-down") is last even with affinity!
	if cands[0].upstream.ID != "u-affinity" || cands[1].upstream.ID != "u-idle" || cands[2].upstream.ID != "u-busy" || cands[3].upstream.ID != "u-down" {
		t.Fatalf("unexpected candidate order: %s, %s, %s, %s", cands[0].upstream.ID, cands[1].upstream.ID, cands[2].upstream.ID, cands[3].upstream.ID)
	}
}

func TestMidStreamIdleTimeoutEmitsErrorAndPassthroughEndpointScope(t *testing.T) {
	stalledUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/embeddings" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"object":"list","data":[]}`)

			return
		}
		if r.URL.Path == "/v1/audio" {
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"ok":true}`)

			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"id\":\"c1\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"half \"}}]}\n\n")
		fl.Flush()
		<-r.Context().Done()
	}))
	defer stalledUpstream.Close()

	epRoute := "/workspace/ws/endpoint/ep-ocr"
	whisperLimit := int64(100)
	snap := &state.ConfigSnapshot{
		Routes: map[string]state.Route{
			epRoute: {
				Key: epRoute, Workspace: "ws", Kind: "internal", Name: "ep-ocr", Managed: true,
				Upstreams: []state.Upstream{{ID: "endpoint:ws/ep-ocr", URL: stalledUpstream.URL, TargetProtocol: protocol.ProtocolOpenAIChat}},
			},
			"/workspace/ws/endpoint/asr": {
				Key: "/workspace/ws/endpoint/asr", Workspace: "ws", Kind: "internal", Name: "asr", Managed: true,
				Upstreams:   []state.Upstream{{ID: "endpoint:ws/endpoint-asr", URL: stalledUpstream.URL, TargetProtocol: protocol.ProtocolOpenAIChat}},
				ModelRoutes: map[string]state.ModelRoute{"whisper": {Targets: []state.Target{{Upstream: "endpoint:ws/endpoint-asr", UpstreamModel: "whisper"}}}},
			},
		},
		APIKeys: map[string]state.APIKey{
			"k-stream":  {ID: "k-stream", Secret: "sk-stream", Workspace: "ws"},
			"k-pinned":  {ID: "k-pinned", Secret: "sk-pinned", Workspace: "ws", AllowedModels: []state.AllowedModel{{Model: "m", Type: "external", EndpointName: "ee-1"}}},
			"k-foo":     {ID: "k-foo", Secret: "sk-foo", Workspace: "ws", AllowedModels: []state.AllowedModel{{Model: "foo"}}},
			"k-whisper": {ID: "k-whisper", Secret: "sk-whisper", Workspace: "ws", AllowedModels: []state.AllowedModel{{Model: "whisper", TokenLimit: &whisperLimit}}},
		},
		Secrets:      map[string]string{"sk-stream": "k-stream", "sk-pinned": "k-pinned", "sk-foo": "k-foo", "sk-whisper": "k-whisper"},
		PolicyGroups: map[string]state.PolicyGroup{},
	}

	leases := quota.NewLeasePool(10, time.Minute)
	engine := state.NewEngine(snap, leases, 4)

	gw, err := NewServer(Config{State: engine, Quota: leases, IdleTimeout: 35 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	gwSrv := httptest.NewServer(gw)
	defer gwSrv.Close()

	reqWhisper, _ := http.NewRequest(http.MethodPost, gwSrv.URL+"/workspace/ws/endpoint/asr/v1/audio", strings.NewReader("audio-bytes"))
	reqWhisper.Header.Set("x-api-key", "sk-whisper")
	reqWhisper.Header.Set("Content-Type", "multipart/form-data; boundary=audio")
	respWhisper, err := http.DefaultClient.Do(reqWhisper)
	if err != nil {
		t.Fatal(err)
	}
	respWhisper.Body.Close()
	if respWhisper.StatusCode != http.StatusOK {
		t.Fatalf("single-model multipart passthrough status: got %d", respWhisper.StatusCode)
	}

	// 1. Key pinned only to external/ee-1 must be blocked from passthrough on internal/ep-ocr.
	reqPinned, _ := http.NewRequest(http.MethodPost, gwSrv.URL+epRoute+"/v1/ocr", strings.NewReader("img"))
	reqPinned.Header.Set("x-api-key", "sk-pinned")
	respPinned, err := http.DefaultClient.Do(reqPinned)
	if err != nil {
		t.Fatal(err)
	}
	respPinned.Body.Close()
	if respPinned.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for passthrough endpoint bypass, got %d", respPinned.StatusCode)
	}

	// 1b. Model-restricted key ("foo") must block "/v1/embeddings" with forbidden model "bar" and non-JSON "/v1/ocr", while permitting model "foo".
	for _, tc := range []struct {
		path, ct, body string
		want           int
	}{
		{"/v1/embeddings", "application/json", `{"model":"bar","input":"x"}`, http.StatusForbidden},
		{"/v1/ocr", "application/octet-stream", "raw-binary", http.StatusForbidden},
		{"/v1/embeddings", "application/json", `{"model":"foo","input":"x"}`, http.StatusOK},
	} {
		reqFoo, _ := http.NewRequest(http.MethodPost, gwSrv.URL+epRoute+tc.path, strings.NewReader(tc.body))
		reqFoo.Header.Set("x-api-key", "sk-foo")
		reqFoo.Header.Set("Content-Type", tc.ct)
		respFoo, doErr := http.DefaultClient.Do(reqFoo)
		if doErr != nil {
			t.Fatal(doErr)
		}
		respFoo.Body.Close()
		if respFoo.StatusCode != tc.want {
			t.Fatalf("passthrough model restriction %s (%s): got %d, want %d", tc.path, tc.body, respFoo.StatusCode, tc.want)
		}
	}

	// 2. Stalled mid-stream upstream must emit an error event rather than normal message_stop!
	reqStall, _ := http.NewRequest(http.MethodPost, gwSrv.URL+epRoute+"/anthropic/v1/messages", strings.NewReader(`{"model":"m","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	reqStall.Header.Set("x-api-key", "sk-stream")
	reqStall.Header.Set("Content-Type", "application/json")
	respStall, err := http.DefaultClient.Do(reqStall)
	if err != nil {
		t.Fatal(err)
	}
	rawOut, _ := io.ReadAll(respStall.Body)
	respStall.Body.Close()

	if !strings.Contains(string(rawOut), "event: error") || strings.Contains(string(rawOut), "event: message_stop") {
		t.Fatalf("expected SSE error event without normal message_stop on idle timeout, got:\n%s", rawOut)
	}
}
