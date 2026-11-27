package state

import (
	"cmp"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/neutree-ai/neutree/internal/icp/event"
)

const (
	EndpointTargets event.Kind = "pod.endpoint_targets"
	BlockHint       event.Kind = "kv.block_hint"
)

type KVHint struct {
	Endpoint, PodUID string
	Hash             uint64
}

// Event keys include the resident pod: two replicas may hold the same block.
func (h KVHint) Event(expires time.Time) event.Event {
	return event.Event{Lane: event.LaneRuntime, Kind: BlockHint, Key: h.Endpoint + "\x00" + h.PodUID + "\x00" + strconv.FormatUint(h.Hash, 16), Value: h, ExpiresAt: expires}
}

type PodTarget struct {
	UID, Replica, URL, RoutingLogic string
	Load                            *BackendLoad
	Passthrough                     bool
}

type membership struct {
	pods  []PodTarget
	until time.Time
	ring  *ringIndex
}

type RuntimeTable struct {
	mu       sync.RWMutex
	members  map[string]membership
	hints    map[KVHint]time.Time
	capacity int
}

func NewRuntime(capacity int) *RuntimeTable {
	return &RuntimeTable{capacity: capacity, members: make(map[string]membership), hints: make(map[KVHint]time.Time)}
}

// EndpointTargets carries a full replacement, including an empty set on deletion.
// BlockHint carries one absolute residency fact; a zero expiry retracts it.
func (t *RuntimeTable) Apply(e event.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()

	switch e.Kind {
	case EndpointTargets:
		pods, ok := e.Value.([]PodTarget)
		if !ok {
			return
		}

		if len(pods) == 0 {
			delete(t.members, e.Key)
			return
		}

		pods = slices.Clone(pods)
		slices.SortFunc(pods, func(a, b PodTarget) int { return cmp.Compare(a.UID, b.UID) })
		old := t.members[e.Key].pods

		for i := range pods {
			if pods[i].RoutingLogic == "" {
				pods[i].RoutingLogic = "roundrobin"
			}

			j, ok := slices.BinarySearchFunc(old, pods[i].UID, func(p PodTarget, id string) int { return cmp.Compare(p.UID, id) })
			if ok {
				pods[i].Load = old[j].Load
			} else {
				pods[i].Load = NewLoad(t.capacity)
			}
		}

		m := t.members[e.Key]
		if !slices.Equal(old, pods) {
			m.pods = pods
			m.ring = &ringIndex{}
		}

		m.until = e.ExpiresAt
		t.members[e.Key] = m
	case BlockHint:
		hint, ok := e.Value.(KVHint)
		if !ok {
			return
		}

		if e.ExpiresAt.IsZero() {
			delete(t.hints, hint)
		} else {
			t.hints[hint] = e.ExpiresAt
		}
	}
}

func (t *RuntimeTable) Expire(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for k, v := range t.members {
		if !v.until.IsZero() && !now.Before(v.until) {
			delete(t.members, k)
		}
	}

	for k, v := range t.hints {
		if !now.Before(v) {
			delete(t.hints, k)
		}
	}
}

type RingPoint struct {
	Hash uint64
	Pod  int
}
type ringIndex struct {
	once   sync.Once
	points []RingPoint
}

type RuntimeView struct {
	ring     *ringIndex
	Pods     []PodTarget
	Endpoint string
	table    *RuntimeTable
	now      time.Time
}

func (t *RuntimeTable) View(endpoint string, now time.Time) RuntimeView {
	t.mu.RLock()
	defer t.mu.RUnlock()
	v := RuntimeView{table: t, now: now, Endpoint: endpoint}
	m := t.members[endpoint]

	if m.until.IsZero() || now.Before(m.until) {
		v.Pods = m.pods
		v.ring = m.ring
	}

	return v
}

// HashRing belongs to this immutable membership, so dropping an endpoint
// also drops its index. Both hash pickers use the same builder.
func (v RuntimeView) HashRing(build func([]PodTarget) []RingPoint) []RingPoint {
	if v.ring == nil {
		return build(v.Pods)
	}

	v.ring.once.Do(func() { v.ring.points = build(v.Pods) })

	return v.ring.points
}

func (v RuntimeView) LookupKVHints(blocks []uint64) map[string]int {
	scores := make(map[string]int)

	v.table.mu.RLock()
	defer v.table.mu.RUnlock()

	for _, p := range v.Pods {
		for _, b := range blocks {
			if v.now.Before(v.table.hints[KVHint{v.Endpoint, p.UID, b}]) {
				scores[p.UID]++
			}
		}
	}

	return scores
}

func (t *RuntimeTable) Endpoints() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()

	out := make([]string, 0, len(t.members))
	for k := range t.members {
		out = append(out, k)
	}

	return out
}

type BackendLoad struct {
	Inflight, Waiting atomic.Int64
	CoolUntil         atomic.Int64
	slots             chan struct{}
	mu                sync.Mutex
	window            [60]sample
}

type sample struct {
	second, arrivals, finished int64
	latency                    time.Duration
}

func NewLoad(limit int) *BackendLoad {
	b := &BackendLoad{}
	if limit > 0 {
		b.slots = make(chan struct{}, limit)
	}

	return b
}
