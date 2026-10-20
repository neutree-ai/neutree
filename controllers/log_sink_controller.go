package controllers

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/observability/logsink"
	"github.com/neutree-ai/neutree/pkg/storage"
)

const (
	logSinkSyncInterval   = 10 * time.Second
	logSinkScrapeInterval = 30 * time.Second
	// How long a delivered sink may stay unconfirmed by Vector before it is Failed.
	logSinkApplyTimeout = 3 * time.Minute
	// How long a sink may fail every send without a single success before it is Failed.
	logSinkFailingTimeout = 5 * time.Minute
	// Vector does not retry a rejected reload, so an unconfirmed delivery is
	// written again: soon after a rejection shows up, otherwise only after a
	// ConfigMap has had time to propagate (up to about a minute). Doubles per
	// attempt up to logSinkRetryMaxWait.
	logSinkRetryRejectedWait    = 30 * time.Second
	logSinkRetryUnconfirmedWait = 90 * time.Second
	logSinkRetryMaxWait         = 5 * time.Minute

	logSinkDisabledMessage = "log sink is disabled; it is not delivered to Vector"
	logSinkNoTraceMessage  = "the AI trace pipeline is not deployed (no built-in trace store), so log sinks have no traces to receive"
)

// LogSinkController renders all LogSinks into one Vector config fragment on every
// pass, so it runs its own loop instead of reconciling objects one by one.
type LogSinkController struct {
	storage              storage.Storage
	syncer               logsink.Syncer
	tracePipelineEnabled bool

	scrape         func(ctx context.Context) (*logsink.VectorMetrics, error)
	testConnection func(ctx context.Context, sink *v1.LogSink) error
	now            func() time.Time

	lastScrapeAt time.Time
	metrics      *logsink.VectorMetrics
	scrapeErr    error
	states       map[string]*logSinkState

	// The last write of the config and the rejected reload count before it.
	lastWriteAt  time.Time
	rejectedBase float64
	retries      int
}

// logSinkState is what the controller remembers about a delivered sink.
type logSinkState struct {
	hash        string
	componentID string
	deliveredAt time.Time
	// written is set when this process wrote the config that delivered the
	// sink; rejectedBase is Vector's rejected reload count before that write.
	written      bool
	rejectedBase float64
	// applied is set once the delivered component ID runs in Vector.
	applied bool

	prev         *logsink.ComponentCounters
	window       *logsink.ComponentCounters
	windowAt     time.Time
	failingSince time.Time
}

type LogSinkControllerOption struct {
	Storage              storage.Storage
	Syncer               logsink.Syncer
	MetricsURL           string
	TracePipelineEnabled bool
}

func NewLogSinkController(option *LogSinkControllerOption) (*LogSinkController, error) {
	if option.Syncer == nil {
		return nil, errors.New("log sink syncer is required")
	}

	httpClient := &http.Client{Timeout: 10 * time.Second}
	metricsURL := option.MetricsURL

	return &LogSinkController{
		storage:              option.Storage,
		syncer:               option.Syncer,
		tracePipelineEnabled: option.TracePipelineEnabled,
		scrape: func(ctx context.Context) (*logsink.VectorMetrics, error) {
			if metricsURL == "" {
				return nil, errors.New("Vector metrics URL is not configured")
			}

			return logsink.ScrapeMetrics(ctx, httpClient, metricsURL)
		},
		testConnection: logsink.TestConnection,
		now:            time.Now,
		states:         map[string]*logSinkState{},
	}, nil
}

func (c *LogSinkController) Name() string {
	return "log-sink"
}

func (c *LogSinkController) Start(ctx context.Context) {
	wait.UntilWithContext(ctx, func(ctx context.Context) {
		if err := c.reconcile(ctx); err != nil {
			klog.Errorf("log sink reconcile failed: %v", err)
		}
	}, logSinkSyncInterval)
}

func (c *LogSinkController) reconcile(ctx context.Context) error {
	now := c.now()

	// Scrape before delivering, so a delivery has a reload baseline to compare against.
	scraped := c.lastScrapeAt.IsZero() || now.Sub(c.lastScrapeAt) >= logSinkScrapeInterval
	if scraped {
		c.lastScrapeAt = now

		m, err := c.scrape(ctx)
		c.scrapeErr = err

		if err != nil {
			klog.V(4).Infof("failed to scrape Vector metrics: %v", err)
		} else {
			c.metrics = m
		}
	}

	sinks, err := c.storage.ListLogSink(storage.ListOption{})
	if err != nil {
		return errors.Wrap(err, "failed to list log sinks")
	}

	sort.Slice(sinks, func(i, j int) bool { return sinks[i].GetName() < sinks[j].GetName() })

	var active []*v1.LogSink

	excluded := map[string]error{}

	for i := range sinks {
		s := &sinks[i]

		switch {
		case s.GetDeletionTimestamp() != "":
		case !s.Spec.IsEnabled():
			excluded[s.GetName()] = errors.New(logSinkDisabledMessage)
		default:
			if err := logsink.Validate(s); err != nil {
				excluded[s.GetName()] = errors.Wrap(err, "invalid spec")
			} else if !c.tracePipelineEnabled {
				excluded[s.GetName()] = errors.New(logSinkNoTraceMessage)
			} else {
				active = append(active, s)
			}
		}
	}

	active = c.assignComponentIDs(active, excluded, now)

	syncErr := c.deliver(ctx, active, now)
	if syncErr != nil {
		klog.Errorf("failed to deliver log sinks to Vector: %v", syncErr)
	}

	if scraped && c.scrapeErr == nil {
		c.observe(now)
	}

	for i := range sinks {
		s := &sinks[i]

		if err := c.reconcileOne(ctx, s, excluded[s.GetName()], syncErr, scraped && c.scrapeErr == nil, now); err != nil {
			klog.Errorf("failed to reconcile log sink %s: %v", s.GetName(), err)
		}
	}

	return nil
}

// assignComponentIDs gives every active sink without one a fresh component ID,
// saved before delivery so it survives a restart. A sink whose ID cannot be
// saved is not delivered.
func (c *LogSinkController) assignComponentIDs(active []*v1.LogSink, excluded map[string]error,
	now time.Time) []*v1.LogSink {
	kept := active[:0]

	for _, s := range active {
		old := s.Status
		if old == nil {
			old = &v1.LogSinkStatus{}
		}

		if logsink.ValidComponentID(old.ComponentID) {
			kept = append(kept, s)
			continue
		}

		status := *old
		status.ComponentID = logsink.NewComponentID(s.GetName())

		if err := c.writeStatus(s, old, &status, now); err != nil {
			excluded[s.GetName()] = errors.Wrap(err, "failed to assign a Vector component ID")
			continue
		}

		kept = append(kept, s)
	}

	return kept
}

// deliver renders the active sinks and records which of them a write delivered.
func (c *LogSinkController) deliver(ctx context.Context, active []*v1.LogSink, now time.Time) error {
	activeNames := map[string]bool{}
	for _, s := range active {
		activeNames[s.GetName()] = true
	}

	for name := range c.states {
		if !activeNames[name] {
			delete(c.states, name)
		}
	}

	rendered, err := logsink.Render(active)
	if err != nil {
		return err
	}

	// After a restart, the config on disk counts as just written.
	if c.lastWriteAt.IsZero() {
		c.lastWriteAt = now
	}

	retry := c.retryDue(now)
	if retry {
		rendered = rendered.WithRetry(now.UTC().Format(time.RFC3339Nano))
	}

	written, err := c.syncer.Sync(ctx, rendered)
	if err != nil {
		return err
	}

	if written {
		c.lastWriteAt = now
		if c.metrics != nil {
			c.rejectedBase = c.metrics.RejectedReloads
		}

		if retry {
			c.retries++
			klog.Infof("Vector has not confirmed the log sink config; delivered it again (attempt %d)", c.retries+1)
		} else {
			c.retries = 0
		}
	}

	for _, s := range active {
		hash := logsink.SpecHash(s)
		id := s.Status.ComponentID

		st := c.states[s.GetName()]
		if st != nil && st.hash == hash && st.componentID == id {
			continue
		}

		next := &logSinkState{hash: hash, componentID: id, deliveredAt: now}
		if st != nil && st.componentID == id {
			next.prev, next.window, next.windowAt = st.prev, st.window, st.windowAt
		}

		// Nothing written means the config on disk already had this sink (e.g.
		// after a core restart); rejections counted before say nothing about it.
		if written && c.metrics != nil {
			next.written = true
			next.rejectedBase = c.metrics.RejectedReloads
		}

		c.states[s.GetName()] = next
	}

	return nil
}

// retryDue reports whether the config should be written again because Vector
// has not confirmed a delivered sink, waiting at least one scrape after the
// last write and backing off per attempt.
func (c *LogSinkController) retryDue(now time.Time) bool {
	if c.metrics == nil || c.scrapeErr != nil || !c.lastScrapeAt.After(c.lastWriteAt) {
		return false
	}

	unconfirmed := false

	for _, st := range c.states {
		if !st.applied {
			unconfirmed = true
			break
		}
	}

	if !unconfirmed {
		return false
	}

	wait := logSinkRetryUnconfirmedWait
	if c.metrics.RejectedSince(c.rejectedBase) {
		wait = logSinkRetryRejectedWait
	}

	for i := 0; i < c.retries && wait < logSinkRetryMaxWait; i++ {
		wait *= 2
	}

	if wait > logSinkRetryMaxWait {
		wait = logSinkRetryMaxWait
	}

	return now.Sub(c.lastWriteAt) >= wait
}

// observe folds a fresh scrape into every delivered sink's state. Only the
// series of the component ID that was delivered count: series of an earlier
// activation stay in the exporter.
func (c *LogSinkController) observe(now time.Time) {
	for _, st := range c.states {
		cur, present := c.metrics.Components[st.componentID]

		// The ID is new for each activation, so only this delivery can make it run.
		if present && cur.Running {
			st.applied = true
		}

		if !present {
			continue
		}

		if st.prev != nil {
			delta := cur.Sub(*st.prev)
			st.window = &delta
			st.windowAt = now

			switch {
			case delta.Sent > 0:
				st.failingSince = time.Time{}
			case delta.Failed > 0 && st.failingSince.IsZero():
				st.failingSince = now
			}
		}

		st.prev = &cur
	}
}

func (c *LogSinkController) reconcileOne(ctx context.Context, s *v1.LogSink, excludedErr, syncErr error,
	scraped bool, now time.Time) error {
	old := s.Status
	if old == nil {
		old = &v1.LogSinkStatus{}
	}

	status := *old

	if s.GetDeletionTimestamp() != "" {
		if old.Phase == v1.LogSinkPhaseDELETED {
			klog.Infof("Log sink %s is deleted, removing it from storage", s.GetName())

			return errors.Wrapf(c.storage.DeleteLogSink(s.GetID()), "failed to delete log sink %s", s.GetName())
		}

		isForceDelete := v1.IsForceDelete(s.Metadata.Annotations)
		if syncErr != nil && !isForceDelete {
			return errors.Wrap(syncErr, "log sink is still delivered to Vector")
		}

		LogForceDeletionWarning(isForceDelete, "log_sink", "", s.GetName(), syncErr)

		status.Phase = v1.LogSinkPhaseDELETED
		status.ErrorMessage = ""

		return c.writeStatus(s, old, &status, now)
	}

	st := c.states[s.GetName()]
	hash := logsink.SpecHash(s)

	switch {
	case !s.Spec.IsEnabled():
		status.Phase = v1.LogSinkPhaseDISABLED
		status.ErrorMessage = ""
		// Not delivered any more; delivering it again takes a new ID.
		status.ComponentID = ""
	case excludedErr != nil:
		status.Phase = v1.LogSinkPhaseFAILED
		status.ErrorMessage = excludedErr.Error()
		status.ComponentID = ""
	case syncErr != nil && (st == nil || st.hash != hash || st.componentID != status.ComponentID || !st.applied):
		status.Phase = v1.LogSinkPhaseFAILED
		status.ErrorMessage = "failed to deliver to Vector: " + syncErr.Error()
	case st == nil:
		// Unreachable: an active sink is either delivered or hit syncErr.
		status.Phase = v1.LogSinkPhasePENDING
		status.ErrorMessage = ""
	default:
		status.Phase, status.ErrorMessage = c.derivePhase(st, now)

		if scraped && st.window != nil && st.windowAt.Equal(now) {
			status.SentEvents += int64(st.window.Sent)
			status.DroppedEvents += int64(st.window.Dropped)

			if st.window.Sent > 0 {
				status.LastSentTime = now.UTC().Format(time.RFC3339Nano)
			}
		}
	}

	// A failed test is reported but does not hold back delivery.
	if excludedErr == nil && old.ObservedHash != hash {
		test := &v1.LogSinkConnectionTest{Time: now.UTC().Format(time.RFC3339Nano), OK: true}
		if err := c.testConnection(ctx, s); err != nil {
			test.OK = false
			test.Message = err.Error()
		}

		status.LastConnectionTest = test
		status.ObservedHash = hash
	}

	return c.writeStatus(s, old, &status, now)
}

func (c *LogSinkController) derivePhase(st *logSinkState, now time.Time) (v1.LogSinkPhase, string) {
	if !st.applied {
		rejected := st.written && c.metrics != nil && c.metrics.RejectedSince(st.rejectedBase)

		if now.Sub(st.deliveredAt) < logSinkApplyTimeout {
			if rejected {
				return v1.LogSinkPhasePENDING, "Vector rejected a config reload, retrying; see the Vector logs"
			}

			return v1.LogSinkPhasePENDING, ""
		}

		msg := fmt.Sprintf("Vector has not loaded this sink within %s of delivery", logSinkApplyTimeout)

		switch {
		case c.scrapeErr != nil:
			msg += "; cannot read Vector metrics: " + c.scrapeErr.Error()
		case rejected:
			msg += "; Vector rejected a config reload, see the Vector logs; still retrying"
		}

		return v1.LogSinkPhaseFAILED, msg
	}

	if !st.failingSince.IsZero() && now.Sub(st.failingSince) >= logSinkFailingTimeout {
		return v1.LogSinkPhaseFAILED, fmt.Sprintf("no events delivered for %s; every send failed", logSinkFailingTimeout)
	}

	if w := st.window; w != nil && (w.Failed > 0 || w.Dropped > 0) {
		return v1.LogSinkPhaseDEGRADED, fmt.Sprintf("in the last %s: %d failed sends, %d dropped events",
			logSinkScrapeInterval, int64(w.Failed), int64(w.Dropped))
	}

	return v1.LogSinkPhaseRUNNING, ""
}

// writeStatus skips the write when nothing visible changed, to avoid churn.
func (c *LogSinkController) writeStatus(s *v1.LogSink, old, status *v1.LogSinkStatus, now time.Time) error {
	if old.Phase == status.Phase &&
		old.ErrorMessage == status.ErrorMessage &&
		old.ObservedHash == status.ObservedHash &&
		old.SentEvents == status.SentEvents &&
		old.DroppedEvents == status.DroppedEvents &&
		old.LastSentTime == status.LastSentTime &&
		old.ComponentID == status.ComponentID &&
		sameConnectionTest(old.LastConnectionTest, status.LastConnectionTest) {
		return nil
	}

	if old.Phase != status.Phase || status.LastTransitionTime == "" {
		status.LastTransitionTime = now.UTC().Format(time.RFC3339Nano)
	}

	// status is a composite column: every field is sent, or the omitted ones are nulled.
	if err := c.storage.UpdateLogSink(strconv.Itoa(s.ID), &v1.LogSink{Status: status}); err != nil {
		return errors.Wrapf(err, "failed to update log sink %s status", s.GetName())
	}

	s.Status = status

	return nil
}

func sameConnectionTest(a, b *v1.LogSinkConnectionTest) bool {
	if a == nil || b == nil {
		return a == b
	}

	return *a == *b
}
