package router

import (
	"testing"
	"time"

	"github.com/neutree-ai/neutree/internal/icp/event"
	"github.com/neutree-ai/neutree/internal/icp/state"
)

func TestAffinityAndOverload(t *testing.T) {
	rt := state.NewRuntime(0)
	rt.Apply(event.Event{Kind: state.EndpointTargets, Key: "ep", Value: []state.PodTarget{{UID: "a", URL: "a"}, {UID: "b", URL: "b"}}, ExpiresAt: time.Now().Add(time.Hour)})
	v := rt.View("ep", time.Now())
	r := NewRegistry()
	sig := RouteSignals{Session: "client"}
	first, _ := r["static_hash"].Pick(sig, v)
	// One request on the preferred replica leaves room for a second.
	first.Load.Inflight.Store(1)
	plain := state.RuntimeView{Pods: v.Pods}
	if next, err := r.Resolve("session").Pick(sig, plain); err != nil || next.UID != first.UID {
		t.Fatalf("low-load affinity changed: got %s, err %v", next.UID, err)
	}
	first.Load.Inflight.Store(2)
	if next, _ := r["static_hash"].Pick(sig, v); next.UID != first.UID {
		t.Fatal("static moved")
	}
	if next, _ := r["consistent_hash"].Pick(sig, v); next.UID == first.UID {
		t.Fatal("bounded ring overloaded")
	}
	sig.ExcludeUID = first.UID
	p, _ := r["rr"].Pick(sig, v)
	p.Load.CoolUntil.Store(time.Now().Add(time.Minute).UnixNano())
	for _, policy := range []string{"unknown", "consistent_hash"} {
		if _, err := r.Resolve(policy).Pick(sig, v); err != ErrNoBackend {
			t.Fatal(policy, err)
		}
	}
}
