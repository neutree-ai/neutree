package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/neutree-ai/neutree/internal/icp/protocol"
	"github.com/neutree-ai/neutree/internal/icp/proxy"
	"github.com/neutree-ai/neutree/internal/icp/quota"
	"github.com/neutree-ai/neutree/internal/icp/state"
)

type usageViewResp struct {
	Pools        []quota.PoolState   `json:"pools"`
	ActiveLeases []quota.Lease       `json:"active_leases"`
	Bills        []quota.UsageRecord `json:"bills"`
}

func startGatewayHarness(t *testing.T, cfg gatewayConfig) (*gatewayApp, *httptest.Server, *httptest.Server) {
	t.Helper()

	app, err := newGatewayApp(cfg)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	go app.engine.Run()

	if err = app.initialize(ctx, cfg); err != nil {
		cancel()
		app.engine.Close()
		t.Fatal(err)
	}

	dataSrv := httptest.NewServer(h2c.NewHandler(app.buildDataMux(), &http2.Server{}))
	adminSrv := httptest.NewServer(app.buildAdminMux())

	t.Cleanup(func() {
		dataSrv.Close()
		adminSrv.Close()
		cancel()
		app.engine.Close()
	})

	return app, dataSrv, adminSrv
}

func postAdminEvent(t *testing.T, adminURL, adminToken string, ev state.Event) {
	t.Helper()

	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest(http.MethodPost, adminURL+"/admin/v1/events", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}

	req.Header.Set("Content-Type", "application/json")

	if adminToken != "" {
		req.Header.Set(headerAdminToken, adminToken)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("admin event failed (%d): %s", resp.StatusCode, body)
	}
}

func fetchUsageView(t *testing.T, adminURL, adminToken string) usageViewResp {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, adminURL+"/admin/v1/usage", nil)
	if err != nil {
		t.Fatal(err)
	}

	if adminToken != "" {
		req.Header.Set(headerAdminToken, adminToken)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /admin/v1/usage, got %d", resp.StatusCode)
	}

	var out usageViewResp
	if err = json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}

	return out
}

func TestEndToEndGatewayAcceptance(t *testing.T) {
	var kongFirstChunkSeen atomic.Bool

	kongUploadGate := make(chan struct{})
	legacyKong := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer legacy-client-cred" || r.URL.RawQuery != "mode=raw" {
			t.Errorf("legacy fallback altered headers/query: auth=%q query=%q", r.Header.Get("Authorization"), r.URL.RawQuery)
		}

		buf := make([]byte, 32)
		n, _ := r.Body.Read(buf)
		if n > 0 {
			kongFirstChunkSeen.Store(true)
			close(kongUploadGate)
		}

		rest, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Legacy-Upstream", "kong")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(append(buf[:n], rest...))
	}))
	defer legacyKong.Close()

	const adminTok = "adm-secret-1"
	_, dataSrv, adminSrv := startGatewayHarness(t, gatewayConfig{
		legacyURL:      legacyKong.URL,
		managedRoutes:  "none",
		callbackSecret: "cb-hmac-secret",
		adminToken:     adminTok,
		leaseStep:      4,
		leaseTTL:       time.Minute,
	})

	// Stage 1: Unmanaged route streams multipart zero-buffered to legacy Kong before upload finishes.
	verifyStage1LegacyZeroBuffer(t, dataSrv.URL, kongUploadGate, &kongFirstChunkSeen)

	// Stage 2: Enable heterogeneous route via live Config event; Responses stream fails over Chat 503 -> Anthropic SSE with 2 attempt records.
	verifyStage2HeterogeneousFailover(t, dataSrv.URL, adminSrv.URL, adminTok)

	// Stage 3: Multimodal multipart passthrough + all 3 neutral usage layers (Header, SSE tail frame, signed async HMAC callback).
	verifyStage3MultimodalAndThreeLayerUsage(t, dataSrv.URL, adminSrv.URL, adminTok)

	// Stage 4: Mid-stream quota exhaustion cancels upstream context and emits native SSE error event.
	verifyStage4MidStreamQuotaCutoff(t, dataSrv.URL, adminSrv.URL, adminTok)

	// Stage 5: Live Runtime & Quota events switch prefix-cache affinity and quota state in milliseconds.
	verifyStage5LiveEventsAndPrefixAffinity(t, dataSrv.URL, adminSrv.URL, adminTok)

	// Stage 6: Read-only /admin/v1/telemetry exposes aggregated request, retry, TTFT, and settled usage traces.
	telReq, _ := http.NewRequest(http.MethodGet, adminSrv.URL+"/admin/v1/telemetry", nil)
	telReq.Header.Set(headerAdminToken, adminTok)

	telResp, err := http.DefaultClient.Do(telReq)
	if err != nil {
		t.Fatal(err)
	}

	var tel proxy.TelemetrySnapshot
	if err = json.NewDecoder(telResp.Body).Decode(&tel); err != nil {
		_ = telResp.Body.Close()
		t.Fatal(err)
	}
	_ = telResp.Body.Close()

	if telResp.StatusCode != http.StatusOK || tel.Completed < 5 || tel.Retries < 1 || len(tel.RecentRequests) == 0 {
		t.Fatalf("unexpected /admin/v1/telemetry snapshot: %+v", tel)
	}
}

func verifyStage1LegacyZeroBuffer(t *testing.T, dataURL string, uploadGate <-chan struct{}, sawFirstChunk *atomic.Bool) {
	t.Helper()

	pr, pw := io.Pipe()
	mp := multipart.NewWriter(pw)

	errCh := make(chan error, 1)
	go func() {
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", `form-data; name="audio"; filename="clip.wav"`)
		h.Set("Content-Type", "application/octet-stream")

		part, err := mp.CreatePart(h)
		if err != nil {
			errCh <- err
			return
		}

		_, _ = part.Write([]byte("first-streaming-chunk-0123456789abcdef"))
		<-uploadGate
		_, _ = part.Write([]byte("-second-streaming-chunk"))
		_ = mp.Close()
		errCh <- pw.Close()
	}()

	req, _ := http.NewRequest(http.MethodPost, dataURL+"/workspace/default/endpoint/legacy-only/v1/audio?mode=raw", pr)
	req.Header.Set("Authorization", "Bearer legacy-client-cred")
	req.Header.Set("Content-Type", mp.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer resp.Body.Close()

	if err = <-errCh; err != nil {
		t.Fatal(err)
	}

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated || !sawFirstChunk.Load() || resp.Header.Get("X-Legacy-Upstream") != "kong" {
		t.Fatalf("stage 1 zero-buffer legacy fallback failed: status=%d body=%q", resp.StatusCode, body)
	}

	if !strings.Contains(string(body), "first-streaming-chunk") || !strings.Contains(string(body), "second-streaming-chunk") {
		t.Fatalf("stage 1 multipart payload corrupted: %q", body)
	}
}

func verifyStage2HeterogeneousFailover(t *testing.T, dataURL, adminURL, adminTok string) {
	t.Helper()

	var chatAttempts, antAttempts atomic.Int32

	chat503 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chatAttempts.Add(1)
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer openai-up-key" {
			t.Errorf("unexpected primary chat request: path=%s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		http.Error(w, `{"error":"overloaded"}`, http.StatusServiceUnavailable)
	}))
	defer chat503.Close()

	antOK := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		antAttempts.Add(1)
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "ant-up-key" {
			t.Errorf("unexpected secondary anthropic request: path=%s key=%q", r.URL.Path, r.Header.Get("x-api-key"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_e2e\",\"model\":\"claude-sonnet-4\",\"usage\":{\"input_tokens\":12}}}\n\n")
		fl.Flush()
		_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Heterogeneous SSE OK\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		_, _ = io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":8}}\n\n")
		_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		fl.Flush()
	}))
	defer antOK.Close()

	routeKey := "/workspace/default/external-endpoint/hetero"
	quotaLimit := int64(200)
	postAdminEvent(t, adminURL, adminTok, state.Event{
		Kind: state.EventConfig,
		Config: &state.ConfigEvent{
			ManagedRoutes: map[string]bool{routeKey: true},
			APIKeys:       []state.APIKey{{ID: "k-e2e", Secret: "sk-e2e", Workspace: "default", TokenQuota: &quotaLimit}},
			Routes: []state.Route{{
				Key: routeKey, Workspace: "default", Kind: "external", Name: "hetero", Managed: true,
				Upstreams: []state.Upstream{
					{ID: "hetero#chat", URL: chat503.URL + "/v1", TargetProtocol: protocol.ProtocolOpenAIChat, AuthType: "bearer", Credential: "openai-up-key"},
					{ID: "hetero#ant", URL: antOK.URL + "/v1", TargetProtocol: protocol.ProtocolAnthropicMessages, AuthType: "api_key", Credential: "ant-up-key"},
				},
				ModelRoutes: map[string]state.ModelRoute{
					"smart-router": {
						MaxAttempts: 1,
						Targets: []state.Target{
							{Upstream: "hetero#chat", UpstreamModel: "gpt-4o", Priority: 1},
							{Upstream: "hetero#ant", UpstreamModel: "claude-sonnet-4", Priority: 2},
						},
					},
				},
			}},
		},
	})

	req, _ := http.NewRequest(http.MethodPost, dataURL+routeKey+"/v1/responses", strings.NewReader(`{"model":"smart-router","stream":true,"input":"Ping"}`))
	req.Header.Set("Authorization", "Bearer sk-e2e")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Neutree-Request-Id", "req-stage2")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	rawSSE, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK || chatAttempts.Load() != 1 || antAttempts.Load() != 1 {
		t.Fatalf("stage 2 failover unexpected: status=%d chat=%d ant=%d body=%s", resp.StatusCode, chatAttempts.Load(), antAttempts.Load(), rawSSE)
	}

	sseText := string(rawSSE)
	if !strings.Contains(sseText, "response.created") || !strings.Contains(sseText, "Heterogeneous SSE OK") || !strings.Contains(sseText, "response.completed") {
		t.Fatalf("stage 2 translated Responses SSE missing frames: %s", sseText)
	}

	view := fetchUsageView(t, adminURL, adminTok)
	var try1Found, try2Found bool

	for _, b := range view.Bills {
		if b.AttemptID == "req-stage2-try-1" && len(b.PoolIDs) == 0 {
			try1Found = true
		}

		if b.AttemptID == "req-stage2-try-2" && quota.BillableUnits(b.Measures) == 20 {
			try2Found = true
		}
	}

	if !try1Found || !try2Found || len(view.Pools) != 1 || view.Pools[0].Used != 20 {
		t.Fatalf("stage 2 dual-attempt accounting mismatch: %+v", view)
	}
}

func verifyStage3MultimodalAndThreeLayerUsage(t *testing.T, dataURL, adminURL, adminTok string) {
	t.Helper()

	var capturedToken atomic.Value

	internalMM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tok := r.Header.Get(headerUsageToken); tok != "" {
			capturedToken.Store(tok)
		}

		_, _ = io.Copy(io.Discard, r.Body)

		if r.URL.Path == "/v1/ocr/header" {
			w.Header().Set("X-Neutree-Usage", `{"measures":[{"metric":"image.pages","unit":"pages","quantity":3},{"metric":"quota.credits","unit":"credits","quantity":15}]}`)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"pages":3}`)

			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"chunk\":\"mm-ok\"}\n\n")
		fl.Flush()
		_, _ = io.WriteString(w, "event: neutree.usage\ndata: {\"measures\":[{\"metric\":\"audio.seconds\",\"unit\":\"s\",\"quantity\":10},{\"metric\":\"quota.credits\",\"unit\":\"credits\",\"quantity\":12}]}\n\n")
		fl.Flush()
	}))
	defer internalMM.Close()

	mmRoute := "/workspace/default/endpoint/mm"
	postAdminEvent(t, adminURL, adminTok, state.Event{
		Kind: state.EventConfig,
		Config: &state.ConfigEvent{
			ManagedRoutes: map[string]bool{mmRoute: true},
			Routes: []state.Route{{
				Key: mmRoute, Workspace: "default", Kind: "internal", Name: "mm", Managed: true,
				Upstreams: []state.Upstream{{ID: "endpoint:default/mm", URL: internalMM.URL, TargetProtocol: protocol.ProtocolOpenAIChat, TrustedUsage: true}},
			}},
		},
	})

	// 3a) Non-streaming X-Neutree-Usage header stripped & settled (+15 credits -> Used=35).
	req1, _ := http.NewRequest(http.MethodPost, dataURL+mmRoute+"/v1/ocr/header", strings.NewReader("raw-bytes"))
	req1.Header.Set("Authorization", "Bearer sk-e2e")
	req1.Header.Set("Content-Type", "application/octet-stream")

	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatal(err)
	}

	_ = resp1.Body.Close()

	if resp1.StatusCode != http.StatusOK || resp1.Header.Get("X-Neutree-Usage") != "" {
		t.Fatalf("stage 3a header usage was not stripped: status=%d hdr=%q", resp1.StatusCode, resp1.Header.Get("X-Neutree-Usage"))
	}

	// 3b) Multipart passthrough + SSE tail usage frame stripped & settled (+12 credits -> Used=47).
	var mpBuf bytes.Buffer
	mw := multipart.NewWriter(&mpBuf)
	fw, _ := mw.CreateFormFile("file", "sample.flac")
	_, _ = fw.Write(bytes.Repeat([]byte("a"), 64*1024))
	_ = mw.Close()

	req2, _ := http.NewRequest(http.MethodPost, dataURL+mmRoute+"/v1/audio/stream", &mpBuf)
	req2.Header.Set("Authorization", "Bearer sk-e2e")
	req2.Header.Set("Content-Type", mw.FormDataContentType())

	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}

	sseBody, _ := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()

	if strings.Contains(string(sseBody), "neutree.usage") || !strings.Contains(string(sseBody), `"mm-ok"`) {
		t.Fatalf("stage 3b SSE usage frame leaked or data lost: %s", sseBody)
	}

	// 3c) Signed async callback adjusts attempt from 12 -> 25 (+13 delta -> Used=60); forged token rejected.
	forgedReq, _ := http.NewRequest(http.MethodPost, dataURL+"/internal/v1/usage", strings.NewReader(`{"measures":[{"metric":"quota.credits","unit":"credits","quantity":1}]}`))
	forgedReq.Header.Set(headerUsageToken, "forged.token")

	forgedResp, err := http.DefaultClient.Do(forgedReq)
	if err != nil {
		t.Fatal(err)
	}

	_ = forgedResp.Body.Close()

	if forgedResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("stage 3c expected 401 on forged token, got %d", forgedResp.StatusCode)
	}

	signedTok, _ := capturedToken.Load().(string)
	cbReq, _ := http.NewRequest(http.MethodPost, dataURL+"/internal/v1/usage", strings.NewReader(`{"measures":[{"metric":"quota.credits","unit":"credits","quantity":25}]}`))
	cbReq.Header.Set(headerUsageToken, signedTok)

	cbResp, err := http.DefaultClient.Do(cbReq)
	if err != nil {
		t.Fatal(err)
	}

	_ = cbResp.Body.Close()

	if cbResp.StatusCode != http.StatusOK {
		t.Fatalf("stage 3c valid callback failed: %d", cbResp.StatusCode)
	}

	if view := fetchUsageView(t, adminURL, adminTok); view.Pools[0].Used != 60 {
		t.Fatalf("stage 3 expected cumulative pool Used=60 (20+15+25), got %+v", view.Pools)
	}
}

func verifyStage4MidStreamQuotaCutoff(t *testing.T, dataURL, adminURL, adminTok string) {
	t.Helper()

	upstreamCancelled := make(chan struct{})
	slowUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)

		for i := 0; i < 20; i++ {
			select {
			case <-r.Context().Done():
				close(upstreamCancelled)
				return
			default:
			}

			_, _ = io.WriteString(w, "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"token \"}}]}\n\n")
			fl.Flush()
			time.Sleep(5 * time.Millisecond)
		}
	}))
	defer slowUpstream.Close()

	tightLimit := int64(68) // Current Used=60, leaseStep=4 -> admits 4, next step hits 68, 3rd step exhausts!
	cutoffRoute := "/workspace/default/endpoint/cutoff"
	postAdminEvent(t, adminURL, adminTok, state.Event{
		Kind:  state.EventQuota,
		Quota: &quota.QuotaUpdate{PoolID: quota.KeyPoolID("k-e2e"), Limit: &tightLimit},
	})
	postAdminEvent(t, adminURL, adminTok, state.Event{
		Kind: state.EventConfig,
		Config: &state.ConfigEvent{
			ManagedRoutes: map[string]bool{cutoffRoute: true},
			Routes: []state.Route{{
				Key: cutoffRoute, Workspace: "default", Kind: "internal", Name: "cutoff", Managed: true,
				Upstreams: []state.Upstream{{ID: "endpoint:default/cutoff", URL: slowUpstream.URL + "/v1", TargetProtocol: protocol.ProtocolOpenAIChat}},
			}},
		},
	})

	req, _ := http.NewRequest(http.MethodPost, dataURL+cutoffRoute+"/v1/chat/completions", strings.NewReader(`{"model":"qwen","stream":true,"messages":[{"role":"user","content":"go"}]}`))
	req.Header.Set("Authorization", "Bearer sk-e2e")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	select {
	case <-upstreamCancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("stage 4 upstream context was not cancelled on mid-stream quota exhaustion")
	}

	if !strings.Contains(string(raw), `"error"`) {
		t.Fatalf("stage 4 expected native SSE error frame on quota cutoff, got: %s", raw)
	}

	if view := fetchUsageView(t, adminURL, adminTok); len(view.ActiveLeases) != 0 {
		t.Fatalf("stage 4 leaked active leases after cutoff: %+v", view.ActiveLeases)
	}
}

func verifyStage5LiveEventsAndPrefixAffinity(t *testing.T, dataURL, adminURL, adminTok string) {
	t.Helper()

	var nodeAHits, nodeBHits atomic.Int32

	makeNode := func(counter *atomic.Int32, tag string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			counter.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"chat-1","model":"qwen","choices":[{"index":0,"message":{"role":"assistant","content":"`+tag+`"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`)
		}))
	}

	nodeA := makeNode(&nodeAHits, "node-a")
	defer nodeA.Close()

	nodeB := makeNode(&nodeBHits, "node-b")
	defer nodeB.Close()

	generousLimit := int64(500)
	affRoute := "/workspace/default/endpoint/affinity"
	postAdminEvent(t, adminURL, adminTok, state.Event{
		Kind:  state.EventQuota,
		Quota: &quota.QuotaUpdate{PoolID: quota.KeyPoolID("k-e2e"), Limit: &generousLimit},
	})
	postAdminEvent(t, adminURL, adminTok, state.Event{
		Kind: state.EventConfig,
		Config: &state.ConfigEvent{
			ManagedRoutes: map[string]bool{affRoute: true},
			Routes: []state.Route{{
				Key: affRoute, Workspace: "default", Kind: "internal", Name: "affinity", Managed: true,
				Upstreams: []state.Upstream{
					{ID: "endpoint:default/node-a", URL: nodeA.URL + "/v1", TargetProtocol: protocol.ProtocolOpenAIChat},
					{ID: "endpoint:default/node-b", URL: nodeB.URL + "/v1", TargetProtocol: protocol.ProtocolOpenAIChat},
				},
				ModelRoutes: map[string]state.ModelRoute{
					"qwen": {
						Targets: []state.Target{
							{Upstream: "endpoint:default/node-a", UpstreamModel: "qwen", Priority: 1},
							{Upstream: "endpoint:default/node-b", UpstreamModel: "qwen", Priority: 2},
						},
					},
				},
			}},
		},
	})

	// Push live Runtime event advertising warm prefix-cache on node-b (priority 2 beats priority 1 on affinity hit!).
	saltedPrefix := "default:" + affRoute + ":hot-session"
	postAdminEvent(t, adminURL, adminTok, state.Event{
		Kind: state.EventRuntime,
		Runtime: &state.RuntimeEvent{
			UpstreamID:   "endpoint:default/node-b",
			PrefixHashes: []string{saltedPrefix},
			ExpiresAt:    time.Now().Add(time.Minute),
		},
	})

	req, _ := http.NewRequest(http.MethodPost, dataURL+affRoute+"/v1/chat/completions", strings.NewReader(`{"model":"qwen","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer sk-e2e")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Neutree-Prefix-Hash", "hot-session")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK || nodeBHits.Load() != 1 || nodeAHits.Load() != 0 || !strings.Contains(string(body), "node-b") {
		t.Fatalf("stage 5 prefix-cache affinity did not steer to node-b: status=%d a=%d b=%d body=%s", resp.StatusCode, nodeAHits.Load(), nodeBHits.Load(), body)
	}
}

func TestSeedInitAdminTokenAndPublicIsolation(t *testing.T) {
	seedPath := filepath.Join(t.TempDir(), "seed.json")
	seedJSON := `{"api_keys":[{"id":"k-seed","secret":"sk-seed","workspace":"default"}]}`
	if err := os.WriteFile(seedPath, []byte(seedJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	_, dataSrv, adminSrv := startGatewayHarness(t, gatewayConfig{
		seedFile:      seedPath,
		managedRoutes: "*",
		adminToken:    "secret-adm",
		leaseStep:     8,
		leaseTTL:      time.Minute,
	})

	// Public data port must NOT expose /admin/v1/snapshot.
	pubAdminResp, err := http.Get(dataSrv.URL + "/admin/v1/snapshot")
	if err != nil {
		t.Fatal(err)
	}

	_ = pubAdminResp.Body.Close()

	if pubAdminResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 on public /admin/v1/snapshot, got %d", pubAdminResp.StatusCode)
	}

	// Admin port without token must reject with 401; with Bearer token must return seeded key without leaking secret.
	unauthResp, err := http.Get(adminSrv.URL + "/admin/v1/snapshot")
	if err != nil {
		t.Fatal(err)
	}

	_ = unauthResp.Body.Close()

	if unauthResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 on unauthenticated admin request, got %d", unauthResp.StatusCode)
	}

	authReq, _ := http.NewRequest(http.MethodGet, adminSrv.URL+"/admin/v1/snapshot", nil)
	authReq.Header.Set("Authorization", "Bearer secret-adm")

	authResp, err := http.DefaultClient.Do(authReq)
	if err != nil {
		t.Fatal(err)
	}

	snapRaw, _ := io.ReadAll(authResp.Body)
	_ = authResp.Body.Close()

	if authResp.StatusCode != http.StatusOK || !strings.Contains(string(snapRaw), `"k-seed"`) {
		t.Fatalf("expected seeded key in snapshot, got %d: %s", authResp.StatusCode, snapRaw)
	}
}

func TestPostgRESTStartupSyncUsesAPISchema(t *testing.T) {
	postgrest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.Header.Get("Accept-Profile") != "api" {
			http.Error(w, `{"code":"PGRST106"}`, http.StatusNotAcceptable)
			return
		}

		if r.Method == http.MethodPost && r.Header.Get("Content-Profile") != "api" {
			http.Error(w, `{"code":"PGRST106"}`, http.StatusNotAcceptable)
			return
		}

		w.Header().Set("Content-Type", "application/json")

		if r.URL.Path == "/rpc/api_key_owner_active" {
			_, _ = w.Write([]byte(`true`))
			return
		}

		if r.URL.Path == "/api_keys" {
			_, _ = w.Write([]byte(`[{"id":"k-pg","user_id":"u-1","metadata":{"name":"k1","workspace":"default"},"status":{"phase":"Created","sk_value":"sk-pg"}}]`))
			return
		}

		_, _ = w.Write([]byte(`[]`))
	}))
	defer postgrest.Close()

	app, _, _ := startGatewayHarness(t, gatewayConfig{
		postgrestURL:  postgrest.URL,
		jwtSecret:     "0123456789abcdef0123456789abcdef",
		managedRoutes: "*",
		syncInterval:  time.Hour,
		leaseStep:     8,
		leaseTTL:      time.Minute,
	})

	snap := app.engine.Snapshot()
	if snap.Version != 2 || snap.APIKeys["k-pg"].ID != "k-pg" {
		t.Fatalf("expected single SyncAndWait commit (Version=2) with synced PostgREST key, got version=%d keys=%+v", snap.Version, snap.APIKeys)
	}
}
