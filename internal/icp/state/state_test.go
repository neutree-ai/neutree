package state

import (
	"context"
	"testing"
	"time"

	"github.com/neutree-ai/neutree/internal/icp/event"
)

func TestSnapshotPublication(t *testing.T) {
	var s Store
	if err := s.Apply(event.Event{Kind: ConfigReplace, Value: ConfigSnapshot{Routes: map[string]Route{"ep": {RoutingLogic: "kv_aware"}}}}); err != nil {
		t.Fatal(err)
	}
	old := s.Config()
	if err := s.Apply(event.Event{Kind: ConfigReplace, Value: ConfigSnapshot{Kong: ":bad"}}); err == nil || s.Config() != old {
		t.Fatal("bad config replaced live view")
	}
	_ = s.Apply(event.Event{Kind: QuotaLevel, Key: "key", Value: int64(12)})
	if old.Quotas["key"] != 0 || s.Config().Quotas["key"] != 12 || s.Config().Routes["ep"].RoutingLogic != "kv_aware" {
		t.Fatal("snapshot isolation")
	}
}

func TestRuntimeMembershipAndResidency(t *testing.T) {
	now := time.Now()
	rt := NewRuntime(1)
	pods := func(ids ...string) {
		var ps []PodTarget
		for _, uid := range ids {
			ps = append(ps, PodTarget{UID: uid})
		}
		rt.Apply(event.Event{Kind: EndpointTargets, Key: "ep", Value: ps, ExpiresAt: now.Add(time.Minute)})
	}
	pods("a", "b")
	for _, id := range []string{"a", "b"} {
		rt.Apply((KVHint{Endpoint: "ep", Hash: 3, PodUID: id}).Event(now.Add(time.Second)))
	}
	v := rt.View("ep", now)
	load := v.Pods[0].Load
	if err := load.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	pods("a", "b")
	if rt.View("ep", now).Pods[0].Load != load {
		t.Fatal("lost inflight on renewal")
	}
	if score := v.LookupKVHints([]uint64{3}); score["a"] != 1 || score["b"] != 1 {
		t.Fatal(score)
	}
	pods("b")
	if score := rt.View("ep", now).LookupKVHints([]uint64{3}); len(score) != 1 || score["b"] != 1 {
		t.Fatal(score)
	}
	if score := rt.View("ep", now.Add(time.Second)).LookupKVHints([]uint64{3}); len(score) != 0 {
		t.Fatal(score)
	}
	load.Release()
	load.Arrive(now)
	load.Finish(now, now.Add(time.Second))
	if q, lat := load.Stats(now.Add(time.Second)); q != 1./60 || lat != 1 {
		t.Fatal(q, lat)
	}
	if q, lat := load.Stats(now.Add(62 * time.Second)); q != 0 || lat != -1 {
		t.Fatal(q, lat)
	}
}

func TestRuntimeMembershipExpiry(t *testing.T) {
	now := time.Now()
	rt := NewRuntime(1)
	for ep, expiry := range map[string]time.Time{"watch": {}, "temporary": now.Add(time.Minute)} {
		rt.Apply(event.Event{Kind: EndpointTargets, Key: ep, Value: []PodTarget{{UID: "pod"}}, ExpiresAt: expiry})
	}
	later := now.Add(time.Hour)
	if len(rt.View("temporary", later).Pods) != 0 {
		t.Fatal("expired endpoint visible before sweep")
	}
	rt.Expire(later)
	if len(rt.View("watch", later).Pods) != 1 || len(rt.Endpoints()) != 1 {
		t.Fatal("incorrect members after sweep", rt.Endpoints())
	}
}
