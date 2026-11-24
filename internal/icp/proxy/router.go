package proxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/neutree-ai/neutree/internal/icp/state"
)

type candidate struct {
	upstream  state.Upstream
	target    state.Target
	runtime   state.RuntimeState
	affinity  bool
	saturated bool
}

// chooseCandidates resolves and sorts candidate upstreams for (route, model) by:
// 1) Healthy -> 2) Within MaxInflight -> 3) PrefixHash affinity hit -> 4) Priority -> 5) Lowest (QueueDepth + InFlight).
func chooseCandidates(runtime *state.RuntimeTable, route state.Route, model, prefixHash string) ([]candidate, bool) {
	var targets []state.Target

	if model != "" {
		if mr, ok := route.ModelRoutes[model]; ok {
			targets = append(targets, mr.Targets...)
		} else if mrWild, okWild := route.ModelRoutes["*"]; okWild {
			targets = append(targets, mrWild.Targets...)
		} else if len(route.ModelRoutes) > 0 && route.Kind == "external" {
			return nil, false
		}
	}

	if len(targets) == 0 {
		for _, u := range route.Upstreams {
			targets = append(targets, state.Target{Upstream: u.ID, UpstreamModel: model})
		}
	}

	byID := make(map[string]state.Upstream, len(route.Upstreams))
	for _, u := range route.Upstreams {
		byID[u.ID] = u
	}

	result := make([]candidate, 0, len(targets))

	for _, t := range targets {
		u, ok := byID[t.Upstream]
		if !ok {
			continue
		}

		rt := state.RuntimeState{Healthy: true}
		if runtime != nil {
			rt = runtime.Get(u.ID)
		}

		hit := false

		if prefixHash != "" {
			for _, h := range rt.PrefixHashes {
				if h == prefixHash {
					hit = true
					break
				}
			}
		}

		maxInf := t.MaxInflight
		if maxInf <= 0 {
			maxInf = u.MaxInflight
		}

		result = append(result, candidate{
			upstream:  u,
			target:    t,
			runtime:   rt,
			affinity:  hit,
			saturated: maxInf > 0 && rt.InFlight >= maxInf,
		})
	}

	sort.SliceStable(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if a.runtime.Healthy != b.runtime.Healthy {
			return a.runtime.Healthy
		}

		if a.saturated != b.saturated {
			return !a.saturated
		}

		if a.affinity != b.affinity {
			return a.affinity
		}

		if a.target.Priority != b.target.Priority {
			if a.target.Priority == 0 {
				return false
			}

			if b.target.Priority == 0 {
				return true
			}

			return a.target.Priority < b.target.Priority
		}

		return a.runtime.QueueDepth+a.runtime.InFlight < b.runtime.QueueDepth+b.runtime.InFlight
	})

	return result, true
}

func isRetryableStatus(status int, conditions []string) bool {
	if len(conditions) == 0 {
		return status == 429 || status == 502 || status == 503 || status == 504
	}

	code := fmt.Sprint(status)

	for _, c := range conditions {
		c = strings.ToLower(strings.TrimSpace(c))
		if c == code || (c == "5xx" && status >= 500 && status <= 599) {
			return true
		}
	}

	return false
}

func isRetryableError(err error, conditions []string) bool {
	if err == nil {
		return false
	}

	var netErr net.Error
	isTimeout := errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout())

	if len(conditions) == 0 {
		return true
	}

	for _, c := range conditions {
		c = strings.ToLower(strings.TrimSpace(c))
		if isTimeout && (c == "first_byte_timeout" || c == "timeout") {
			return true
		}

		if !isTimeout && c == "connect_error" {
			return true
		}
	}

	return false
}
