package quota

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	ErrQuotaExceeded = errors.New("quota exceeded")
	ErrLeaseNotFound = errors.New("lease not found")
)

type Measure struct {
	Metric   string `json:"metric"`
	Unit     string `json:"unit"`
	Quantity int64  `json:"quantity"`
}

type UsageRecord struct {
	RequestID  string    `json:"request_id"`
	AttemptID  string    `json:"attempt_id"`
	APIKeyID   string    `json:"api_key_id"`
	PoolIDs    []string  `json:"pool_ids,omitempty"`
	RouteKey   string    `json:"route_key,omitempty"`
	UpstreamID string    `json:"upstream_id,omitempty"`
	Model      string    `json:"model,omitempty"`
	Measures   []Measure `json:"measures"`
	Source     string    `json:"source"`
	CreatedAt  time.Time `json:"created_at"`
}

func (r UsageRecord) Quantity(metric, unit string) int64 {
	for _, m := range r.Measures {
		if m.Metric == metric && m.Unit == unit {
			return m.Quantity
		}
	}

	return 0
}

func KeyPoolID(keyID string) string {
	return "key:" + keyID
}

func KeyModelPoolID(keyID, rule string) string {
	return "key-model:" + keyID + ":" + rule
}

func GroupPoolID(groupID string) string {
	return "group:" + groupID
}

type QuotaUpdate struct {
	PoolID string `json:"pool_id"`
	Limit  *int64 `json:"limit,omitempty"`
	Delta  int64  `json:"delta,omitempty"`
	Frozen *bool  `json:"frozen,omitempty"`
}

type Lease struct {
	ID        string    `json:"id"`
	RequestID string    `json:"request_id"`
	AttemptID string    `json:"attempt_id"`
	PoolIDs   []string  `json:"pool_ids"`
	Reserved  int64     `json:"reserved"`
	Consumed  int64     `json:"consumed"`
	ExpiresAt time.Time `json:"expires_at"`
	cancel    context.CancelFunc
	ctx       context.Context
}

func (l *Lease) Context() context.Context {
	return l.ctx
}

type PoolState struct {
	PoolID string `json:"pool_id"`
	Limit  int64  `json:"limit"`
	Used   int64  `json:"used"`
	Frozen bool   `json:"frozen"`
}

type attemptBill struct {
	amount int64
	pools  []string
}

type LeasePool struct {
	mu       sync.Mutex
	leases   map[string]*Lease
	pools    map[string]*PoolState
	bills    map[string]UsageRecord
	attempts map[string]attemptBill
	seq      uint64
	stepSize int64
	ttl      time.Duration
}

// BillableUnits converts protocol-neutral measures into standard quota units.
// Explicit "quota.credits" overrides raw sums; mirror/aggregate breakdown metrics
// (such as total_tokens or cache_read_input_tokens) are skipped to avoid double-charging.
// Returns -1 if any measure has a negative quantity.
func BillableUnits(measures []Measure) int64 {
	var credits int64
	hasCredits := false
	var total int64

	for _, m := range measures {
		if m.Quantity < 0 {
			return -1
		}

		if m.Metric == "quota.credits" {
			hasCredits = true
			credits += m.Quantity

			continue
		}

		switch m.Metric {
		case "total_tokens", "cache_creation_input_tokens", "cache_read_input_tokens", "cached_tokens", "reasoning_tokens":
			continue
		default:
			total += m.Quantity
		}
	}

	if hasCredits {
		return credits
	}

	return total
}

func NewLeasePool(stepSize int64, ttl time.Duration) *LeasePool {
	if stepSize <= 0 {
		stepSize = 1
	}

	if ttl <= 0 {
		ttl = time.Minute
	}

	return &LeasePool{
		leases:   make(map[string]*Lease),
		pools:    make(map[string]*PoolState),
		bills:    make(map[string]UsageRecord),
		attempts: make(map[string]attemptBill),
		stepSize: stepSize,
		ttl:      ttl,
	}
}

func (p *LeasePool) Configure(u QuotaUpdate) {
	if u.PoolID == "" {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	pool := p.poolLocked(u.PoolID)
	if u.Limit != nil {
		pool.Limit = *u.Limit
	}

	pool.Used += u.Delta
	if pool.Used < 0 {
		pool.Used = 0
	}

	if u.Frozen != nil {
		pool.Frozen = *u.Frozen
	}

	if u.Frozen != nil && *u.Frozen {
		for _, l := range p.leases {
			for _, id := range l.PoolIDs {
				if id == u.PoolID && l.cancel != nil {
					l.cancel()
				}
			}
		}
	}
}

func (p *LeasePool) poolLocked(id string) *PoolState {
	v := p.pools[id]
	if v == nil {
		v = &PoolState{PoolID: id}
		p.pools[id] = v
	}

	return v
}

func (p *LeasePool) availableLocked(ids []string) int64 {
	remaining := int64(-1)

	for _, id := range ids {
		pool := p.poolLocked(id)
		if pool.Frozen || (pool.Limit > 0 && pool.Used >= pool.Limit) {
			return 0
		}

		if pool.Limit > 0 && (remaining < 0 || pool.Limit-pool.Used < remaining) {
			remaining = pool.Limit - pool.Used
		}
	}

	return remaining
}

func (p *LeasePool) Reserve(ctx context.Context, requestID, attemptID string, poolIDs []string, estimate int64) (*Lease, error) {
	if estimate <= 0 {
		estimate = p.stepSize
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	p.reapExpiredLocked(now)

	available := p.availableLocked(poolIDs)
	if available == 0 || (available > 0 && available < estimate) {
		return nil, ErrQuotaExceeded
	}

	p.seq++
	id := fmt.Sprintf("lease-%d", p.seq)
	leaseCtx, cancel := context.WithCancel(ctx)
	l := &Lease{
		ID:        id,
		RequestID: requestID,
		AttemptID: attemptID,
		PoolIDs:   append([]string(nil), poolIDs...),
		Reserved:  estimate,
		ExpiresAt: now.Add(p.ttl),
		cancel:    cancel,
		ctx:       leaseCtx,
	}
	p.leases[id] = l
	p.adjustLocked(poolIDs, estimate)

	return l, nil
}

func (p *LeasePool) adjustLocked(ids []string, delta int64) {
	for _, id := range ids {
		pool := p.poolLocked(id)
		pool.Used += delta

		if pool.Used < 0 {
			pool.Used = 0
		}
	}
}
