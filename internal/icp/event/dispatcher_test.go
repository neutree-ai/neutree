package event

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestRuntimeCoalescingAndLaneIsolation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := NewDispatcher(2)
	put := func(kind Kind, key string, n int) {
		t.Helper()
		if err := d.Emit(ctx, Event{Lane: LaneRuntime, Kind: kind, Key: key, Value: n}); err != nil {
			t.Fatal(err)
		}
	}
	put("pods", "ep", 1)
	put("pods", "ep", 2)
	for i := 0; i < 50; i++ {
		put("hint", fmt.Sprint(i), i)
	}
	out := make(chan Event, 3)
	ordered := make(chan Event, 2)
	gate := make(chan struct{})
	defer close(gate)
	go d.Run(ctx, func(e Event) { ordered <- e }, func(e Event) {
		out <- e
		select {
		case <-gate:
		case <-ctx.Done():
		}
	}, func(time.Time) {})
	if first := <-out; first.Kind != "pods" || first.Value != 2 {
		t.Fatal(first)
	}
	for _, lane := range []Lane{LaneConfig, LaneQuota} {
		_ = d.Emit(ctx, Event{Lane: lane})
		select {
		case <-ordered:
		case <-time.After(time.Second):
			t.Fatal("runtime blocked ordered writer")
		}
	}
	for _, want := range []int{48, 49} {
		gate <- struct{}{}
		if got := <-out; got.Value != want {
			t.Fatalf("hint %v, want %d", got, want)
		}
	}
}
