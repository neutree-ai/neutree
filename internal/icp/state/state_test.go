package state

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/neutree-ai/neutree/internal/icp/quota"
	"github.com/neutree-ai/neutree/pkg/storage"
)

func TestConfigGraphRuntimeAndEmptySecretGuard(t *testing.T) {
	p := quota.NewLeasePool(1, time.Minute)
	e := NewEngine(nil, p, 4)

	// Fresh unseen upstream defaults to Healthy: true.
	if !e.Runtime("unseen").Healthy {
		t.Fatal("unseen upstream must default to healthy")
	}

	// QueueDepth/PrefixHashes update with nil Healthy must NOT mark healthy node unhealthy.
	if err := e.Publish(Event{Kind: EventRuntime, Runtime: &RuntimeEvent{UpstreamID: "u-q", QueueDepth: 3, PrefixHashes: []string{"h1"}, ExpiresAt: time.Now().Add(time.Minute)}}); err != nil {
		t.Fatal(err)
	}
	e.Drain()
	if rt := e.Runtime("u-q"); !rt.Healthy || rt.QueueDepth != 3 {
		t.Fatalf("partial runtime update broke Healthy default: %+v", rt)
	}

	// Explicit Healthy: false with zero ExpiresAt must stay unhealthy.
	unhealthy := false
	if err := e.Publish(Event{Kind: EventRuntime, Runtime: &RuntimeEvent{UpstreamID: "u-down", Healthy: &unhealthy}}); err != nil {
		t.Fatal(err)
	}
	e.Drain()
	if e.Runtime("u-down").Healthy {
		t.Fatal("explicit Healthy=false without TTL was overwritten")
	}

	// Empty secret key must NOT allow bare unauthenticated request; explicit empty AllowedModels [] must deny all.
	if err := e.Publish(Event{Kind: EventConfig, Config: &ConfigEvent{
		APIKeys: []APIKey{
			{ID: "pending-key", Secret: "", Workspace: "w"},
			{ID: "deny-all-key", Secret: "sk-deny", Workspace: "w", AllowedModels: []AllowedModel{}},
			{ID: "k", Secret: "sk", Workspace: "w", PolicyGroupIDs: []string{"g"}},
		},
		PolicyGroups: []PolicyGroup{{
			ID:            "g",
			AllowedModels: []GroupModelRule{{Model: "m", Workspace: "w"}},
		}},
		Routes: []Route{{Key: "/workspace/w/endpoint/e", Workspace: "w", Managed: true}},
	}}); err != nil {
		t.Fatal(err)
	}
	e.Drain()
	s := e.Snapshot()

	bareReq := httptest.NewRequest("POST", "http://x/workspace/w/endpoint/e/v1/chat/completions", nil)
	if _, ok := ExtractAPIKey(bareReq, s); ok {
		t.Fatal("bare request without credentials bypassed auth via empty secret")
	}
	if Allowed(s, s.APIKeys["deny-all-key"], "w", "m", "internal", "e") {
		t.Fatal("explicit empty AllowedModels slice must deny all")
	}
	if !Allowed(s, s.APIKeys["k"], "w", "m", "internal", "e") || Allowed(s, s.APIKeys["k"], "other-ws", "m", "internal", "e") {
		t.Fatal("workspace-scoped model check failed")
	}

	// Group policy restricted to ws2 must deny requests in w.
	if err := e.Publish(Event{Kind: EventConfig, Config: &ConfigEvent{
		PolicyGroups: []PolicyGroup{{
			ID:            "g",
			AllowedModels: []GroupModelRule{{Model: "m", Workspace: "ws2"}},
		}},
	}}); err != nil {
		t.Fatal(err)
	}
	e.Drain()
	if Allowed(e.Snapshot(), e.Snapshot().APIKeys["k"], "w", "m", "internal", "e") {
		t.Fatal("workspace-scoped PolicyGroup allowlist failed to narrow access")
	}

	release := e.RuntimeTable().AcquireInFlight("u-ttl")
	defer release()
	old := e.Snapshot().Version
	if err := e.Publish(Event{Kind: EventRuntime, Runtime: &RuntimeEvent{UpstreamID: "u-ttl", Healthy: &unhealthy, ExpiresAt: time.Now().Add(20 * time.Millisecond)}}); err != nil {
		t.Fatal(err)
	}
	e.Drain()
	if e.Snapshot().Version != old {
		t.Fatal("runtime changed config snapshot")
	}
	time.Sleep(25 * time.Millisecond)
	if rt := e.Runtime("u-ttl"); !rt.Healthy || rt.InFlight != 1 {
		t.Fatalf("expired runtime did not recover cleanly: %+v", rt)
	}
}

func TestAPIKeyHeaderAndRouteOwnership(t *testing.T) {
	s := &ConfigSnapshot{
		LegacyFallbackEnabled: true,
		ManagedRoutes:         map[string]bool{"/workspace/w/endpoint/e": true},
		Secrets:               map[string]string{"sk": "k"},
		APIKeys:               map[string]APIKey{"k": {ID: "k"}},
		Routes: map[string]Route{
			"/workspace/w/endpoint/e":  {Key: "/workspace/w/endpoint/e", Managed: true},
			"/workspace/w/endpoint/e2": {Key: "/workspace/w/endpoint/e2", Managed: true},
		},
	}
	r := httptest.NewRequest("GET", "http://x/workspace/w/endpoint/e/v1/models", nil)
	r.Header.Set("Authorization", "Bearer sk")
	if _, ok := ExtractAPIKey(r, s); !ok {
		t.Fatal("bearer key not extracted")
	}
	matched, ok := s.MatchRoute(r.URL.Path)
	if !ok || matched.SubPath != "/v1/models" {
		t.Fatalf("route match unexpected: %+v %v", matched, ok)
	}
	if _, ok := s.MatchRoute("/workspace/w/endpoint/e2/v1/models"); ok {
		t.Fatal("unmanaged route under legacy fallback should not match")
	}
}

func TestQuotaEventUpdatesLeasePool(t *testing.T) {
	p := quota.NewLeasePool(1, time.Minute)
	e := NewEngine(nil, p, 2)
	limit := int64(20)
	if err := e.Publish(Event{Kind: EventQuota, Quota: &quota.QuotaUpdate{PoolID: quota.KeyPoolID("k"), Limit: &limit}}); err != nil {
		t.Fatal(err)
	}
	e.Drain()
	l, err := p.Reserve(context.Background(), "r", "a", []string{quota.KeyPoolID("k")}, 10)
	if err != nil {
		t.Fatal(err)
	}
	frozen := true
	if err := e.Publish(Event{Kind: EventQuota, Quota: &quota.QuotaUpdate{PoolID: quota.KeyPoolID("k"), Frozen: &frozen}}); err != nil {
		t.Fatal(err)
	}
	e.Drain()
	select {
	case <-l.Context().Done():
	default:
		t.Fatal("quota event did not freeze lease")
	}
}

func TestPostgRESTSyncerEndToEnd(t *testing.T) {
	failGroups := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Errorf("missing bearer jwt on %s", r.URL.Path)
		}
		if r.Method == http.MethodGet && r.Header.Get("Accept-Profile") != "api" {
			t.Errorf("expected Accept-Profile: api on %s, got %q", r.URL.Path, r.Header.Get("Accept-Profile"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/clusters":
			_, _ = w.Write([]byte(`[{"metadata":{"name":"c1","workspace":"ws1"},"spec":{"type":"ssh"},"status":{"dashboard_url":"http://10.0.0.8:8265"}}]`))
		case "/endpoints":
			_, _ = w.Write([]byte(`[{"metadata":{"name":"ep1","workspace":"ws1"},"spec":{"cluster":"c1"},"status":{"phase":"Running","service_url":"http://kong-loop:8000/workspace/ws1/endpoint/ep1","served_models":["qwen-7b"]}}]`))
		case "/external_endpoints":
			_, _ = w.Write([]byte(`[{"metadata":{"name":"ee1","workspace":"ws1"},"spec":{"upstreams":[{"name":"claude","upstream":{"url":"https://api.anthropic.com/v1"},"auth":{"type":"api_key","credential":"ant-secret"},"model_mapping":{"legacy-only":"claude-3-5"}},{"name":"local","endpoint_ref":"ep1"},{"name":"broken","endpoint_ref":"missing-ep"}],"model_routes":[{"model":"smart","max_attempts":1,"retryable_conditions":["429","503"],"targets":[{"upstream":"claude","upstream_model":"claude-sonnet","priority":1,"weight":100},{"upstream":"local","upstream_model":"qwen-7b","priority":2,"weight":100},{"upstream":"broken","upstream_model":"ghost","priority":3,"weight":100}]}]}}]`))
		case "/api_keys":
			_, _ = w.Write([]byte(`[{"id":"key-1","user_id":"u-1","metadata":{"name":"k1","workspace":"ws1"},"spec":{"limits":{"token_quota":{"limit":500},"allowed_models":[{"model":"smart","type":"external","endpoint_name":"ee1"},{"model":"qwen-7b","type":"internal"}]}},"status":{"phase":"Created","sk_value":"sk-real-123"}},{"id":"key-del","user_id":"u-1","metadata":{"name":"kdel","workspace":"ws1","deletion_timestamp":"2026-01-01T00:00:00Z"},"status":{"phase":"Created","sk_value":"sk-deleted"}}]`))
		case "/group_policies":
			if failGroups {
				http.Error(w, `{"message":"db down"}`, http.StatusInternalServerError)
				return
			}
			_, _ = w.Write([]byte(`[{"metadata":{"name":"gp-u1"},"spec":{"subject_kind":"User","subject_name":"u-1","denied_models":[{"model":"qwen-7b","workspace":"ws1"}],"token_quota":{"limit":300}}}]`))
		case "/rpc/api_key_owner_active":
			_, _ = w.Write([]byte(`true`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	st, err := storage.New(storage.Options{AccessURL: srv.URL, Scheme: "api", JwtSecret: "0123456789abcdef0123456789abcdef"})
	if err != nil {
		t.Fatal(err)
	}
	leases := quota.NewLeasePool(10, time.Minute)
	engine := NewEngine(&ConfigSnapshot{
		LegacyFallbackEnabled: true,
		ManagedRoutes:         map[string]bool{"/workspace/ws1/external-endpoint/ee1": true},
		APIKeys:               map[string]APIKey{}, Secrets: map[string]string{}, PolicyGroups: map[string]PolicyGroup{}, Routes: map[string]Route{},
	}, leases, 4)
	syncer := &PostgRESTSyncer{Storage: st, Engine: engine}
	if err := syncer.Sync(); err != nil {
		t.Fatal(err)
	}
	engine.Drain()

	snap := engine.Snapshot()
	if _, exists := snap.APIKeys["key-del"]; exists {
		t.Fatal("soft-deleted API key should have been skipped")
	}

	epRoute := snap.Routes["/workspace/ws1/endpoint/ep1"]
	if len(epRoute.Upstreams) != 1 || epRoute.Upstreams[0].ID != "endpoint:ws1/ep1" || epRoute.Upstreams[0].URL != "http://10.0.0.8:8000/ws1/ep1" {
		t.Fatalf("expected scoped cluster serve address, got %+v", epRoute.Upstreams)
	}

	eeRoute, ok := snap.MatchRoute("/workspace/ws1/external-endpoint/ee1/v1/responses")
	if !ok || len(eeRoute.Upstreams) != 2 || eeRoute.Upstreams[0].TargetProtocol != "anthropic_messages" || eeRoute.Upstreams[1].ID != "endpoint:ws1/ep1" {
		t.Fatalf("unexpected external route translation: %+v (ok=%v)", eeRoute, ok)
	}
	if _, leakedLegacy := eeRoute.ModelRoutes["legacy-only"]; leakedLegacy {
		t.Fatal("ModelMapping leaked into ModelRoutes when Spec.ModelRoutes was present")
	}
	smartTargets := eeRoute.ModelRoutes["smart"].Targets
	if len(smartTargets) != 2 || smartTargets[0].Upstream != "/workspace/ws1/external-endpoint/ee1#claude" || smartTargets[1].Upstream != "endpoint:ws1/ep1" {
		t.Fatalf("unexpected resolved ModelRoute targets: %+v", smartTargets)
	}

	key := snap.APIKeys["key-1"]
	if !Allowed(snap, key, "ws1", "smart", "external", "ee1") || Allowed(snap, key, "ws1", "qwen-7b", "internal", "ep1") {
		t.Fatalf("unexpected key/group policy evaluation: %+v", key)
	}
	pools := PoolIDsForRequest(snap, key, "smart", "external", "ee1")
	if _, err := leases.Reserve(context.Background(), "r1", "a1", pools, 400); err != quota.ErrQuotaExceeded {
		t.Fatalf("expected group 300 limit to cap 500 key quota, got %v", err)
	}

	// Verify group_policies query failure returns error rather than fail-open wiping policies.
	failGroups = true
	if err := syncer.Sync(); err == nil {
		t.Fatal("expected Sync to fail closed when group_policies query errors")
	}
}
