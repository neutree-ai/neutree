package controllers

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/observability/logsink"
	"github.com/neutree-ai/neutree/pkg/storage"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

type fakeLogSinkSyncer struct {
	last    *logsink.Rendered
	err     error
	syncs   int
	written int
}

func (f *fakeLogSinkSyncer) Sync(_ context.Context, r *logsink.Rendered) (bool, error) {
	f.syncs++
	if f.err != nil {
		return false, f.err
	}

	if f.last != nil && string(f.last.Content) == string(r.Content) {
		return false, nil
	}

	f.last = r
	f.written++

	return true, nil
}

// logSinkHarness keeps LogSinks in memory behind the storage mock and drives
// the controller with a fake clock, syncer and Vector metrics.
type logSinkHarness struct {
	t       *testing.T
	c       *LogSinkController
	db      map[int]*v1.LogSink
	deleted []int
	updates int
	syncer  *fakeLogSinkSyncer
	metrics *logsink.VectorMetrics
	now     time.Time
	tests   int
	testErr error
}

func newLogSinkHarness(t *testing.T, sinks ...*v1.LogSink) *logSinkHarness {
	h := &logSinkHarness{
		t:       t,
		db:      map[int]*v1.LogSink{},
		syncer:  &fakeLogSinkSyncer{},
		metrics: &logsink.VectorMetrics{Reloads: 1, Components: map[string]logsink.ComponentCounters{}},
		now:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	for _, s := range sinks {
		h.db[s.ID] = s
	}

	st := storagemocks.NewMockStorage(t)
	st.On("ListLogSink", mock.Anything).Return(func(_ storage.ListOption) ([]v1.LogSink, error) {
		var out []v1.LogSink
		for _, s := range h.db {
			out = append(out, *s)
		}

		return out, nil
	}).Maybe()
	st.On("UpdateLogSink", mock.Anything, mock.Anything).Return(func(id string, data *v1.LogSink) error {
		n, _ := strconv.Atoi(id)
		cp := *data.Status
		h.db[n].Status = &cp
		h.updates++

		return nil
	}).Maybe()
	st.On("DeleteLogSink", mock.Anything).Return(func(id string) error {
		n, _ := strconv.Atoi(id)
		delete(h.db, n)
		h.deleted = append(h.deleted, n)

		return nil
	}).Maybe()

	c, err := NewLogSinkController(&LogSinkControllerOption{Storage: st, Syncer: h.syncer, TracePipelineEnabled: true})
	require.NoError(t, err)

	c.now = func() time.Time { return h.now }
	c.scrape = func(context.Context) (*logsink.VectorMetrics, error) {
		cp := *h.metrics
		cp.Components = map[string]logsink.ComponentCounters{}
		for k, v := range h.metrics.Components {
			cp.Components[k] = v
		}

		return &cp, nil
	}
	c.testConnection = func(context.Context, *v1.LogSink) error {
		h.tests++
		return h.testErr
	}
	h.c = c

	return h
}

func (h *logSinkHarness) pass(advance time.Duration) {
	h.now = h.now.Add(advance)
	require.NoError(h.t, h.c.reconcile(context.Background()))
}

func (h *logSinkHarness) status(id int) *v1.LogSinkStatus {
	require.NotNil(h.t, h.db[id].Status)
	return h.db[id].Status
}

func newTestLogSink(id int, name string) *v1.LogSink {
	return &v1.LogSink{
		ID:       id,
		Metadata: &v1.Metadata{Name: name},
		Spec: &v1.LogSinkSpec{
			Type:         v1.LogSinkTypeVictoriaLogs,
			VictoriaLogs: &v1.VictoriaLogsSinkSpec{URL: "http://vl:9428"},
		},
	}
}

func TestLogSinkController_Lifecycle(t *testing.T) {
	h := newLogSinkHarness(t, newTestLogSink(1, "a"))
	id := logsink.ComponentID("a")

	// Delivered, Vector has not reloaded yet.
	h.pass(0)
	assert.Equal(t, v1.LogSinkPhasePENDING, h.status(1).Phase)
	assert.Contains(t, string(h.syncer.last.Content), id+":")
	assert.True(t, h.status(1).LastConnectionTest.OK)
	assert.Equal(t, logsink.SpecHash(h.db[1]), h.status(1).ObservedHash)
	assert.Equal(t, 1, h.tests)

	// Within the scrape interval nothing changes and nothing is written.
	updates := h.updates
	h.pass(10 * time.Second)
	assert.Equal(t, updates, h.updates)

	// Reload observed and the component exists: Running.
	h.metrics.Reloads = 2
	h.metrics.Components[id] = logsink.ComponentCounters{}
	h.pass(20 * time.Second)
	assert.Equal(t, v1.LogSinkPhaseRUNNING, h.status(1).Phase)
	assert.Empty(t, h.status(1).ErrorMessage)

	// Events delivered: counters and last_sent_time follow.
	h.metrics.Components[id] = logsink.ComponentCounters{Sent: 40}
	h.pass(30 * time.Second)
	assert.Equal(t, v1.LogSinkPhaseRUNNING, h.status(1).Phase)
	assert.Equal(t, int64(40), h.status(1).SentEvents)
	assert.NotEmpty(t, h.status(1).LastSentTime)

	// Failures and drops in the last window: Degraded.
	h.metrics.Components[id] = logsink.ComponentCounters{Sent: 50, Failed: 2, Dropped: 3}
	h.pass(30 * time.Second)
	assert.Equal(t, v1.LogSinkPhaseDEGRADED, h.status(1).Phase)
	assert.Contains(t, h.status(1).ErrorMessage, "2 failed sends, 3 dropped events")
	assert.Equal(t, int64(50), h.status(1).SentEvents)
	assert.Equal(t, int64(3), h.status(1).DroppedEvents)

	// Only failures, no successful send for 5 minutes: Failed.
	failed := 2.0
	for i := 0; i < 11; i++ {
		failed++
		h.metrics.Components[id] = logsink.ComponentCounters{Sent: 50, Failed: failed, Dropped: 3}
		h.pass(30 * time.Second)
	}

	assert.Equal(t, v1.LogSinkPhaseFAILED, h.status(1).Phase)
	assert.Contains(t, h.status(1).ErrorMessage, "no events delivered")

	// One successful send recovers it.
	h.metrics.Components[id] = logsink.ComponentCounters{Sent: 51, Failed: failed, Dropped: 3}
	h.pass(30 * time.Second)
	assert.Equal(t, v1.LogSinkPhaseRUNNING, h.status(1).Phase)

	// Same spec: the connection test is not repeated.
	assert.Equal(t, 1, h.tests)
}

func TestLogSinkController_NotAppliedFails(t *testing.T) {
	h := newLogSinkHarness(t, newTestLogSink(1, "a"))
	h.pass(0)

	// Vector rejects the reload: the counter never moves.
	h.metrics.ConfigLoadErrors = 1
	for i := 0; i < 5; i++ {
		h.pass(30 * time.Second)
	}

	assert.Equal(t, v1.LogSinkPhasePENDING, h.status(1).Phase)

	h.pass(time.Minute)
	assert.Equal(t, v1.LogSinkPhaseFAILED, h.status(1).Phase)
	assert.Contains(t, h.status(1).ErrorMessage, "rejected a config reload")
}

func TestLogSinkController_StaleSeriesNeedReload(t *testing.T) {
	h := newLogSinkHarness(t, newTestLogSink(1, "a"))
	// Series left behind by an earlier sink of the same name.
	h.metrics.Components[logsink.ComponentID("a")] = logsink.ComponentCounters{Sent: 7}

	h.pass(0)
	h.pass(30 * time.Second)
	assert.Equal(t, v1.LogSinkPhasePENDING, h.status(1).Phase)

	h.metrics.Reloads++
	h.pass(30 * time.Second)
	assert.Equal(t, v1.LogSinkPhaseRUNNING, h.status(1).Phase)
}

func TestLogSinkController_SpecChangeRedeliversAndRetests(t *testing.T) {
	h := newLogSinkHarness(t, newTestLogSink(1, "a"))
	h.pass(0)
	h.metrics.Reloads++
	h.metrics.Components[logsink.ComponentID("a")] = logsink.ComponentCounters{}
	h.pass(30 * time.Second)
	require.Equal(t, v1.LogSinkPhaseRUNNING, h.status(1).Phase)

	h.db[1].Spec.VictoriaLogs.Auth = &v1.LogSinkAuth{Type: v1.LogSinkAuthTypeBearer, Token: "new"}
	h.testErr = assert.AnError
	h.pass(10 * time.Second)

	assert.Equal(t, 2, h.syncer.written)
	assert.Equal(t, 2, h.tests)
	assert.False(t, h.status(1).LastConnectionTest.OK)
	assert.Equal(t, logsink.SpecHash(h.db[1]), h.status(1).ObservedHash)
	// A failed test does not hold back delivery.
	assert.Contains(t, string(h.syncer.last.Content), "SECRET[logsink.token.a]")
	assert.Equal(t, v1.LogSinkPhasePENDING, h.status(1).Phase)
}

func TestLogSinkController_DisabledInvalidAndNoTracePipeline(t *testing.T) {
	disabled := newTestLogSink(1, "off")
	disabled.Spec.Enabled = new(bool)

	invalid := newTestLogSink(2, "bad")
	invalid.Spec.VictoriaLogs.URL = "ftp://vl"

	ok := newTestLogSink(3, "ok")

	h := newLogSinkHarness(t, disabled, invalid, ok)
	h.pass(0)

	assert.Equal(t, v1.LogSinkPhaseDISABLED, h.status(1).Phase)
	assert.Empty(t, h.status(1).ErrorMessage)
	assert.Equal(t, v1.LogSinkPhaseFAILED, h.status(2).Phase)
	assert.Contains(t, h.status(2).ErrorMessage, "invalid spec")
	assert.Equal(t, v1.LogSinkPhasePENDING, h.status(3).Phase)

	content := string(h.syncer.last.Content)
	assert.NotContains(t, content, "logsink_off")
	assert.NotContains(t, content, "logsink_bad")
	assert.Contains(t, content, "logsink_ok")
	assert.Equal(t, 1, h.tests, "only the delivered sink is tested")

	h.c.tracePipelineEnabled = false
	h.pass(10 * time.Second)
	assert.Equal(t, v1.LogSinkPhaseFAILED, h.status(3).Phase)
	assert.Equal(t, logSinkNoTraceMessage, h.status(3).ErrorMessage)
	assert.NotContains(t, string(h.syncer.last.Content), "sinks:")
}

func TestLogSinkController_Delete(t *testing.T) {
	keep := newTestLogSink(1, "keep")
	gone := newTestLogSink(2, "gone")
	h := newLogSinkHarness(t, keep, gone)
	h.pass(0)
	require.Contains(t, string(h.syncer.last.Content), "logsink_gone")

	h.db[2].Metadata.DeletionTimestamp = h.now.Format(time.RFC3339)
	h.pass(10 * time.Second)
	assert.NotContains(t, string(h.syncer.last.Content), "logsink_gone")
	assert.Contains(t, string(h.syncer.last.Content), "logsink_keep")
	assert.Equal(t, v1.LogSinkPhaseDELETED, h.status(2).Phase)

	h.pass(10 * time.Second)
	assert.Equal(t, []int{2}, h.deleted)
	assert.Contains(t, h.db, 1)
}

func TestLogSinkController_SyncError(t *testing.T) {
	gone := newTestLogSink(2, "gone")
	gone.Metadata.DeletionTimestamp = "2026-01-01T00:00:00Z"
	gone.Status = &v1.LogSinkStatus{Phase: v1.LogSinkPhaseRUNNING}

	h := newLogSinkHarness(t, newTestLogSink(1, "a"), gone)
	h.syncer.err = assert.AnError
	h.pass(0)

	assert.Equal(t, v1.LogSinkPhaseFAILED, h.status(1).Phase)
	assert.True(t, strings.HasPrefix(h.status(1).ErrorMessage, "failed to deliver to Vector"))
	// Still in Vector's config, so deletion waits...
	assert.Equal(t, v1.LogSinkPhaseRUNNING, h.status(2).Phase)

	// ...unless forced.
	h.db[2].Metadata.Annotations = v1.WithForceDeleteAnnotation(nil)
	h.pass(10 * time.Second)
	assert.Equal(t, v1.LogSinkPhaseDELETED, h.status(2).Phase)
}
