package state

import (
	"context"
	"time"
)

func (b *BackendLoad) Acquire(ctx context.Context) error {
	if b.slots != nil {
		select {
		case b.slots <- struct{}{}:
		default:
			b.Waiting.Add(1)
			defer b.Waiting.Add(-1)
			select {
			case b.slots <- struct{}{}:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}

	b.Inflight.Add(1)

	return nil
}

func (b *BackendLoad) Release() {
	b.Inflight.Add(-1)

	if b.slots != nil {
		<-b.slots
	}
}

func (b *BackendLoad) bucket(now time.Time) *sample {
	s := now.Unix()
	p := &b.window[s%60]

	if p.second != s {
		*p = sample{second: s}
	}

	return p
}

func (b *BackendLoad) Arrive(now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.bucket(now).arrivals++
}

func (b *BackendLoad) Finish(start, end time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p := b.bucket(end)
	p.finished++
	p.latency += end.Sub(start)
}

func (b *BackendLoad) Stats(now time.Time) (qps, avgLatency float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var arrivals, n int64
	var elapsed time.Duration

	for _, s := range b.window {
		if age := now.Unix() - s.second; age >= 0 && age < 60 {
			arrivals += s.arrivals
			n += s.finished
			elapsed += s.latency
		}
	}

	avgLatency = -1
	if n > 0 {
		avgLatency = elapsed.Seconds() / float64(n)
	}

	return float64(arrivals) / 60, avgLatency
}
