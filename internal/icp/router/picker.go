package router

import (
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math/rand/v2"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/neutree-ai/neutree/internal/icp/state"
)

type RouteSignals struct {
	Endpoint, Session string
	PrefixHash        uint64
	HasPrefix         bool
	BlockHashes       []uint64
	ExcludeUID        string // a request has at most one previously attempted backend
}

type EndpointPicker interface {
	Pick(RouteSignals, state.RuntimeView) (state.PodTarget, error)
}

type Registry map[string]EndpointPicker

func NewRegistry() Registry {
	return Registry{"rr": P2CPicker{}, "pow2": P2CPicker{}, "roundrobin": P2CPicker{},
		"session": CHWBLPicker{bounded: true}, "consistent_hash": CHWBLPicker{bounded: true}, "static_hash": CHWBLPicker{}}
}

func (r Registry) Resolve(logic string) EndpointPicker {
	if p := r[logic]; p != nil {
		return p
	}

	return P2CPicker{}
}

var ErrNoBackend = errors.New("no available inference backend")

type P2CPicker struct{}

func (P2CPicker) Pick(s RouteSignals, v state.RuntimeView) (state.PodTarget, error) {
	// Reservoir sampling chooses two distinct eligible candidates without allocating.
	var a, b state.PodTarget
	n := 0

	now := time.Now().UnixNano()
	for _, p := range v.Pods {
		if p.UID == s.ExcludeUID || p.Load.CoolUntil.Load() > now {
			continue
		}

		n++
		switch n {
		case 1:
			a = p
		case 2:
			b = p
		default:
			switch rand.IntN(n) { //nolint:gosec // G404: sampling backends for load balancing
			case 0:
				a = p
			case 1:
				b = p
			}
		}
	}

	if n == 0 {
		return a, ErrNoBackend
	}

	if n > 1 {
		loadA, loadB := a.Load.Inflight.Load(), b.Load.Inflight.Load()
		if loadB < loadA || loadB == loadA && rand.IntN(2) == 0 { //nolint:gosec // G404: break load-balancing ties
			return b, nil
		}
	}

	return a, nil
}

func hash(key string) uint64 {
	sum := sha256.Sum256([]byte(key))
	return binary.BigEndian.Uint64(sum[:8])
}
func buildRing(pods []state.PodTarget) []state.RingPoint {
	pts := make([]state.RingPoint, 0, 100*len(pods))

	for i, p := range pods {
		for n := 0; n < 100; n++ {
			pts = append(pts, state.RingPoint{Hash: hash(p.URL + "#" + strconv.Itoa(n)), Pod: i})
		}
	}

	slices.SortFunc(pts, func(a, b state.RingPoint) int {
		if order := cmp.Compare(a.Hash, b.Hash); order != 0 {
			return order
		}

		return cmp.Compare(pods[a.Pod].URL, pods[b.Pod].URL)
	})

	return pts
}

type CHWBLPicker struct{ bounded bool }

func (p CHWBLPicker) Pick(s RouteSignals, v state.RuntimeView) (state.PodTarget, error) {
	if s.Session == "" && !s.HasPrefix {
		return P2CPicker{}.Pick(s, v)
	}

	if len(v.Pods) == 0 {
		return state.PodTarget{}, ErrNoBackend
	}

	key := s.PrefixHash
	if s.Session != "" {
		key = hash(s.Session)
	}

	pts := v.HashRing(buildRing)
	start := sort.Search(len(pts), func(i int) bool { return pts[i].Hash >= key })
	var total, count int64

	now := time.Now().UnixNano()
	for _, c := range v.Pods {
		if c.UID != s.ExcludeUID && c.Load.CoolUntil.Load() <= now {
			total += c.Load.Inflight.Load()
			count++
		}
	}

	if count == 0 {
		return state.PodTarget{}, ErrNoBackend
	}
	// Include the incoming request and round the per-backend capacity up.
	capacity := ((total+1)*5 + count*4 - 1) / (count * 4)
	var least state.PodTarget

	for i := range pts {
		c := v.Pods[pts[(start+i)%len(pts)].Pod]
		if c.UID == s.ExcludeUID || c.Load.CoolUntil.Load() > now {
			continue
		}

		load := c.Load.Inflight.Load()
		if !p.bounded || load < capacity {
			return c, nil
		}

		if least.Load == nil || load < least.Load.Inflight.Load() {
			least = c
		}
	}

	if least.Load != nil {
		return least, nil
	}

	return least, ErrNoBackend
}
