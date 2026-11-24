package quota

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestUsageExtractionAndCallbackBinding(t *testing.T) {
	h := http.Header{"X-Neutree-Usage": []string{`{"measures":[{"metric":"input_tokens","unit":"tokens","quantity":5},{"metric":"output_tokens","unit":"tokens","quantity":2},{"metric":"total_tokens","unit":"tokens","quantity":7}]}`}}
	r, ok, err := ExtractHeaderUsage(h)
	if err != nil || !ok || BillableUnits(r.Measures) != 7 || h.Get("X-Neutree-Usage") != "" {
		t.Fatalf("header extraction failed: %#v %v %v (billable=%d)", r, ok, err, BillableUnits(r.Measures))
	}

	negHeader := http.Header{"X-Neutree-Usage": []string{`{"measures":[{"metric":"tokens","unit":"unit","quantity":-5}]}`}}
	if _, _, err := ExtractHeaderUsage(negHeader); err == nil {
		t.Fatal("expected negative usage header to fail")
	}

	rec := UsageRecord{
		RequestID:  "r",
		AttemptID:  "a",
		APIKeyID:   "key",
		PoolIDs:    []string{KeyPoolID("key"), GroupPoolID("group")},
		RouteKey:   "route",
		UpstreamID: "up",
		Model:      "m",
	}
	token, err := SignUsageToken(rec, time.Now().Add(time.Minute), []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}

	r, err = VerifyUsageCallback([]byte(`{"api_key_id":"forged","measures":[{"metric":"tokens","unit":"unit","quantity":9}]}`), token, []byte("secret"))
	if err != nil || r.APIKeyID != "key" || len(r.PoolIDs) != 2 || BillableUnits(r.Measures) != 9 || r.Source != "async_callback" {
		t.Fatalf("callback binding failed: %#v %v", r, err)
	}
}

type smallChunkReader struct {
	r    io.Reader
	size int
}

func (s *smallChunkReader) Read(p []byte) (int, error) {
	if len(p) > s.size {
		p = p[:s.size]
	}

	return s.r.Read(p)
}

func TestSSEFilterHandlesSplitFramesCRLFAndBodySubstring(t *testing.T) {
	// Ensure literal "event: neutree.usage" inside normal data payload is NOT stripped as a usage frame.
	raw := "event: message\r\ndata: {\"text\":\"literal event: neutree.usage inside text\"}\r\n\r\nevent:neutree.usage\r\ndata: {\"measures\":[{\"metric\":\"input_tokens\",\"unit\":\"tokens\",\"quantity\":3},{\"metric\":\"total_tokens\",\"unit\":\"tokens\",\"quantity\":3}]}\r\n\r\nevent: message\ndata: end\n\n"
	f := NewSSEUsageFilter(&smallChunkReader{r: strings.NewReader(raw), size: 5})
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}

	out := string(b)
	if !strings.Contains(out, "literal event: neutree.usage inside text") || strings.Contains(out, "event:neutree.usage") || !strings.Contains(out, "data: end") {
		t.Fatalf("unexpected filtered stream: %q", out)
	}

	usage, ok := f.Usage()
	if !ok || BillableUnits(usage.Measures) != 3 {
		t.Fatalf("missing or double-counted SSE usage: %#v %v", usage, ok)
	}
}

func TestLeaseSettleCallbackFirstAndIdempotentCorrection(t *testing.T) {
	p := NewLeasePool(10, time.Minute)
	limit := int64(200)
	pools := []string{KeyPoolID("k"), GroupPoolID("g")}
	p.Configure(QuotaUpdate{PoolID: pools[0], Limit: &limit})
	p.Configure(QuotaUpdate{PoolID: pools[1], Limit: &limit})

	l, err := p.Reserve(context.Background(), "r", "a", pools, 100)
	if err != nil {
		t.Fatal(err)
	}

	if err := p.Settle(l.ID, 100, UsageRecord{RequestID: "r", AttemptID: "a", APIKeyID: "k", PoolIDs: pools, Measures: []Measure{{Metric: "tokens", Unit: "unit", Quantity: 100}}}); err != nil {
		t.Fatal(err)
	}

	delta, err := p.Correct(UsageRecord{RequestID: "r", AttemptID: "a", APIKeyID: "k", PoolIDs: pools, Measures: []Measure{{Metric: "input_tokens", Unit: "tokens", Quantity: 100}, {Metric: "output_tokens", Unit: "tokens", Quantity: 60}, {Metric: "total_tokens", Unit: "tokens", Quantity: 160}}})
	if err != nil || delta != 60 {
		t.Fatalf("correction failed: %d %v", delta, err)
	}

	delta, err = p.Correct(UsageRecord{RequestID: "r", AttemptID: "a", APIKeyID: "forged", Measures: []Measure{{Metric: "quota.credits", Unit: "credits", Quantity: 160}}})
	if err != nil || delta != 0 {
		t.Fatalf("replay correction failed: %d %v", delta, err)
	}

	// Verify async callback arriving BEFORE Settle does not double-charge.
	l2, err := p.Reserve(context.Background(), "r2", "a2", pools, 20)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := p.Correct(UsageRecord{RequestID: "r2", AttemptID: "a2", APIKeyID: "k", PoolIDs: pools, Measures: []Measure{{Metric: "tokens", Unit: "unit", Quantity: 30}}}); err != nil {
		t.Fatal(err)
	}

	if err := p.Settle(l2.ID, 25, UsageRecord{RequestID: "r2", AttemptID: "a2"}); err != nil {
		t.Fatal(err)
	}

	for _, ps := range p.Pools() {
		if ps.Used != 190 {
			t.Fatalf("expected pool %s Used=190 (160+30), got %d", ps.PoolID, ps.Used)
		}
	}
}

func TestFrozenPoolAndTailRemainderAndExpiry(t *testing.T) {
	p := NewLeasePool(100, 15*time.Millisecond)
	limit := int64(150)
	poolID := KeyPoolID("k")
	p.Configure(QuotaUpdate{PoolID: poolID, Limit: &limit})

	l, err := p.Reserve(context.Background(), "r", "a", []string{poolID}, 100)
	if err != nil {
		t.Fatal(err)
	}

	// Consuming 101 (>100 reserved) when stepSize=100 and remaining=50 should grant the 50 tail remainder instead of failing.
	if err := p.StepCharge(context.Background(), l.ID, 101); err != nil {
		t.Fatalf("tail remainder should succeed, got %v", err)
	}

	if err := p.StepCharge(context.Background(), l.ID, 50); err != ErrQuotaExceeded {
		t.Fatalf("expected quota exceeded once 150 exhausted, got %v", err)
	}

	select {
	case <-l.Context().Done():
	default:
		t.Fatal("quota exhaustion did not cancel lease context")
	}

	// Wait for short-lease TTL expiry and verify lazy reap frees reserved 150.
	time.Sleep(20 * time.Millisecond)
	l2, err := p.Reserve(context.Background(), "r2", "a2", []string{poolID}, 50)
	if err != nil {
		t.Fatalf("expired orphan lease was not reaped: %v", err)
	}

	frozen := true
	p.Configure(QuotaUpdate{PoolID: poolID, Frozen: &frozen})
	select {
	case <-l2.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("freeze did not cancel lease context")
	}
}
