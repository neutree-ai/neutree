package quota

import (
	"context"
	"errors"
	"time"
)

func (p *LeasePool) StepCharge(ctx context.Context, leaseID string, deltaUnits int64) error {
	if deltaUnits <= 0 {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	p.reapExpiredLocked(now)

	l := p.leases[leaseID]
	if l == nil {
		return ErrLeaseNotFound
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	nextConsumed := l.Consumed + deltaUnits
	if nextConsumed <= l.Reserved {
		l.Consumed = nextConsumed
		return nil
	}

	realNeed := nextConsumed - l.Reserved
	available := p.availableLocked(l.PoolIDs)

	if available == 0 || (available > 0 && available < realNeed) {
		if l.cancel != nil {
			l.cancel()
		}

		return ErrQuotaExceeded
	}

	l.Consumed = nextConsumed

	grant := realNeed
	if grant < p.stepSize {
		grant = p.stepSize
	}

	if available > 0 && grant > available {
		grant = available
	}

	l.Reserved += grant
	l.ExpiresAt = now.Add(p.ttl)
	p.adjustLocked(l.PoolIDs, grant)

	return nil
}

func (p *LeasePool) Settle(leaseID string, actual int64, record UsageRecord) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.reapExpiredLocked(time.Now())

	l := p.leases[leaseID]
	if l == nil {
		return ErrLeaseNotFound
	}

	if actual < 0 {
		actual = 0
	}

	if len(record.Measures) > 0 {
		if bill := BillableUnits(record.Measures); bill >= 0 {
			actual = bill
		}
	}

	if record.RequestID == "" {
		record.RequestID = l.RequestID
	}

	if record.AttemptID == "" {
		record.AttemptID = l.AttemptID
	}

	if len(record.PoolIDs) == 0 {
		record.PoolIDs = append([]string(nil), l.PoolIDs...)
	}

	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}

	key := attemptKey(record.RequestID, record.AttemptID)
	if _, exists := p.attempts[key]; exists {
		p.adjustLocked(l.PoolIDs, -l.Reserved)
		delete(p.leases, leaseID)

		return nil
	}

	p.adjustLocked(l.PoolIDs, actual-l.Reserved)
	p.bills[key] = record

	copiedPools := append([]string(nil), l.PoolIDs...)
	p.attempts[key] = attemptBill{amount: actual, pools: copiedPools}
	delete(p.leases, leaseID)

	return nil
}

func (p *LeasePool) Correct(record UsageRecord) (int64, error) {
	amount := BillableUnits(record.Measures)
	if amount < 0 {
		return 0, errors.New("negative usage")
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.reapExpiredLocked(time.Now())

	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}

	key := attemptKey(record.RequestID, record.AttemptID)
	old, ok := p.attempts[key]

	if !ok {
		pools := append([]string(nil), record.PoolIDs...)
		p.attempts[key] = attemptBill{amount: amount, pools: pools}
		p.bills[key] = record
		p.adjustLocked(pools, amount)

		return amount, nil
	}

	delta := amount - old.amount
	p.adjustLocked(old.pools, delta)
	record.PoolIDs = append([]string(nil), old.pools...)
	p.attempts[key] = attemptBill{amount: amount, pools: old.pools}
	p.bills[key] = record

	return delta, nil
}

func (p *LeasePool) Cancel(leaseID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	l := p.leases[leaseID]
	if l == nil {
		return ErrLeaseNotFound
	}

	p.adjustLocked(l.PoolIDs, -l.Reserved)

	if l.cancel != nil {
		l.cancel()
	}

	delete(p.leases, leaseID)

	return nil
}

// RecordFailedAttempt logs a failed pre-first-byte upstream attempt in the ledger with nil PoolIDs
// so every attempt is observable and late async callbacks for failed attempts never charge client quota pools.
func (p *LeasePool) RecordFailedAttempt(record UsageRecord) {
	if record.RequestID == "" || record.AttemptID == "" {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.reapExpiredLocked(time.Now())

	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}

	record.PoolIDs = nil

	amount := BillableUnits(record.Measures)
	if amount < 0 {
		amount = 0
	}

	key := attemptKey(record.RequestID, record.AttemptID)
	p.bills[key] = record
	p.attempts[key] = attemptBill{amount: amount, pools: nil}
}

func (p *LeasePool) Bills() []UsageRecord {
	p.mu.Lock()
	defer p.mu.Unlock()

	out := make([]UsageRecord, 0, len(p.bills))
	for _, v := range p.bills {
		out = append(out, v)
	}

	return out
}

func (p *LeasePool) Pools() []PoolState {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.reapExpiredLocked(time.Now())

	out := make([]PoolState, 0, len(p.pools))
	for _, v := range p.pools {
		out = append(out, *v)
	}

	return out
}

func (p *LeasePool) ActiveLeases() []Lease {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.reapExpiredLocked(time.Now())

	out := make([]Lease, 0, len(p.leases))
	for _, v := range p.leases {
		out = append(out, *v)
	}

	return out
}

func (p *LeasePool) reapExpiredLocked(now time.Time) {
	for id, l := range p.leases {
		if now.After(l.ExpiresAt) {
			p.adjustLocked(l.PoolIDs, -l.Reserved)

			if l.cancel != nil {
				l.cancel()
			}

			delete(p.leases, id)
		}
	}
}

func attemptKey(requestID, attemptID string) string {
	return requestID + "\x00" + attemptID
}
