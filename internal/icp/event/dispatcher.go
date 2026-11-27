package event

import (
	"container/list"
	"context"
	"errors"
	"sync"
	"time"
)

type Lane uint8

const (
	LaneConfig Lane = iota
	LaneQuota
	LaneRuntime
)

type Kind string
type Event struct {
	Lane      Lane
	Kind      Kind
	Key       string
	Rev       int64
	ExpiresAt time.Time
	Value     any
}

type Emit func(context.Context, Event) error
type Source interface {
	Name() string
	Run(context.Context, Emit) error
}

type bucket struct {
	pending list.List
	keys    map[string]*list.Element
}

func (b *bucket) pop() Event {
	e, ok := b.pending.Remove(b.pending.Front()).(Event)
	if !ok {
		panic("runtime mailbox contains a non-event")
	}

	delete(b.keys, e.Key)

	return e
}

type Dispatcher struct {
	config, quota chan Event
	wake          chan struct{}
	mu            sync.Mutex
	buckets       map[Kind]*bucket
	// Active kinds rotate after every delivery, including during a KV flood.
	active []Kind
	limit  int
}

func NewDispatcher(capacity int) *Dispatcher {
	return &Dispatcher{config: make(chan Event, capacity), quota: make(chan Event, capacity), wake: make(chan struct{}, 1), limit: capacity, buckets: make(map[Kind]*bucket)}
}

func (d *Dispatcher) Emit(ctx context.Context, e Event) error {
	var ch chan Event

	switch e.Lane {
	case LaneConfig:
		ch = d.config
	case LaneQuota:
		ch = d.quota
	case LaneRuntime:
		if err := ctx.Err(); err != nil {
			return err
		}

		d.mu.Lock()

		b := d.buckets[e.Kind]
		if b == nil {
			b = &bucket{keys: make(map[string]*list.Element)}
			d.buckets[e.Kind] = b
		}

		if old := b.keys[e.Key]; old != nil {
			old.Value = e
			b.pending.MoveToBack(old)
		} else {
			if b.pending.Len() == 0 {
				d.active = append(d.active, e.Kind)
			}

			if b.pending.Len() == d.limit {
				b.pop()
			}

			b.keys[e.Key] = b.pending.PushBack(e)
		}
		d.mu.Unlock()
		select {
		case d.wake <- struct{}{}:
		default:
		}

		return nil
	default:
		return errors.New("unknown event lane")
	}
	select {
	case ch <- e:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *Dispatcher) pop() (Event, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if len(d.active) == 0 {
		return Event{}, false
	}

	kind := d.active[0]
	b := d.buckets[kind]
	e := b.pop()

	d.active = d.active[1:]
	if b.pending.Len() > 0 {
		d.active = append(d.active, kind)
	}

	return e, true
}

// Run owns the two consumers. The ordered writer never interleaves snapshot updates.
func (d *Dispatcher) Run(ctx context.Context, ordered, runtime func(Event), expire func(time.Time)) {
	done := make(chan struct{})
	go func() {
		defer close(done)

		tick := time.NewTicker(time.Second)
		defer tick.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case now := <-tick.C:
				expire(now)
			default:
			}

			if e, ok := d.pop(); ok {
				runtime(e)
				continue
			}
			select {
			case <-ctx.Done():
				return
			case now := <-tick.C:
				expire(now)
			case <-d.wake:
			}
		}
	}()

	defer func() { <-done }()

	for ctx.Err() == nil {
		for i := 0; i < 8; i++ {
			select {
			case e := <-d.quota:
				ordered(e)
				continue
			default:
			}

			break
		}
		select {
		case e := <-d.config:
			ordered(e)
			continue
		default:
		}
		select {
		case e := <-d.config:
			ordered(e)
		case e := <-d.quota:
			ordered(e)
		case <-ctx.Done():
			return
		}
	}
}
