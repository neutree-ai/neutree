package controllers

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/model_registry"
	modelregistrymocks "github.com/neutree-ai/neutree/internal/model_registry/mocks"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

// newCheckedAtController builds a controller with a stopped clock, so what a
// reconcile does with the recorded check time is observable rather than a matter
// of timing.
func newCheckedAtController(t *testing.T, storage *storagemocks.MockStorage,
	registry *modelregistrymocks.MockModelRegistry, now time.Time) *ModelRegistryController {
	t.Helper()

	model_registry.NewModelRegistry = func(obj *v1.ModelRegistry) (model_registry.ModelRegistry, error) {
		return registry, nil
	}

	c := &ModelRegistryController{
		storage: storage,
		stats:   model_registry.StatsAggregator{Now: func() time.Time { return now }},
		now:     func() time.Time { return now },
	}
	c.syncHandler = c.sync

	return c
}

// connectedRegistry is a registry that has been up and measured recently, so
// neither the statistics nor anything else gives the reconcile a reason to write.
func connectedRegistry(now time.Time, checkedAgo time.Duration, connectedAt string) *v1.ModelRegistry {
	return &v1.ModelRegistry{
		ID:       1,
		Metadata: &v1.Metadata{Name: "test", Workspace: "default"},
		Status: &v1.ModelRegistryStatus{
			Phase:              v1.ModelRegistryPhaseCONNECTED,
			LastTransitionTime: connectedAt,
			LastCheckedAt:      now.Add(-checkedAgo).Format(time.RFC3339Nano),
			Stats: &v1.ModelRegistryStats{
				ModelCount:     3,
				StorageBytes:   4096,
				StatsUpdatedAt: now.Format(time.RFC3339Nano),
			},
		},
	}
}

// The registry has been reachable for three days. Its check time keeps moving;
// the moment it came up does not.
func TestModelRegistryController_HealthyRegistryAdvancesOnlyTheCheckTime(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	connectedAt := now.Add(-72 * time.Hour).Format(time.RFC3339Nano)

	mockStorage := &storagemocks.MockStorage{}
	mockRegistry := &modelregistrymocks.MockModelRegistry{}
	// Since #526 every reconcile re-establishes the connection before checking it,
	// so that a mount that vanished underneath a Connected registry is restored
	// rather than merely reported. What this test is about — which status writes
	// that produces — is unchanged.
	mockRegistry.On("Connect").Return(nil)
	mockRegistry.On("Disconnect").Return(nil)
	mockRegistry.On("HealthyCheck").Return(nil)

	var written *v1.ModelRegistryStatus

	mockStorage.On("UpdateModelRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
		obj, ok := args.Get(1).(*v1.ModelRegistry)
		if !ok {
			t.Fatalf("unexpected update payload %T", args.Get(1))
		}

		written = obj.Status
	}).Return(nil)

	c := newCheckedAtController(t, mockStorage, mockRegistry, now)

	// Two minutes since the last recorded check: past the write interval.
	assert.NoError(t, c.sync(connectedRegistry(now, 2*time.Minute, connectedAt)))

	if assert.NotNil(t, written) {
		assert.Equal(t, v1.ModelRegistryPhaseCONNECTED, written.Phase)
		assert.Equal(t, now.Format(time.RFC3339Nano), written.LastCheckedAt)
		assert.Equal(t, connectedAt, written.LastTransitionTime,
			"a re-check that confirms what was already true is not a transition")
		// The reconcile did not re-measure, so the counters have to be carried
		// forward or the composite column is nulled.
		assert.Equal(t, 3, written.Stats.ModelCount)
	}

	mockStorage.AssertExpectations(t)
	mockRegistry.AssertExpectations(t)
}

// The reconcile runs every ten seconds. Persisting each unchanged result would
// be a stream of writes saying nothing.
func TestModelRegistryController_UnchangedResultIsNotRewrittenEveryCycle(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	connectedAt := now.Add(-72 * time.Hour).Format(time.RFC3339Nano)

	mockStorage := &storagemocks.MockStorage{}
	mockRegistry := &modelregistrymocks.MockModelRegistry{}
	mockRegistry.On("Connect").Return(nil)
	mockRegistry.On("Disconnect").Return(nil)
	mockRegistry.On("HealthyCheck").Return(nil)

	c := newCheckedAtController(t, mockStorage, mockRegistry, now)

	assert.NoError(t, c.sync(connectedRegistry(now, 5*time.Second, connectedAt)))

	mockStorage.AssertNotCalled(t, "UpdateModelRegistry", mock.Anything, mock.Anything)
	mockRegistry.AssertExpectations(t)
}

// Both reasons are non-empty, so a check for "is there an error" reports no
// change and leaves a stale reason on display.
func TestModelRegistryController_ChangedFailureReasonIsWritten(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	failedAt := now.Add(-time.Hour).Format(time.RFC3339Nano)

	mockStorage := &storagemocks.MockStorage{}
	mockRegistry := &modelregistrymocks.MockModelRegistry{}
	mockRegistry.On("Disconnect").Return(nil)
	mockRegistry.On("Connect").Return(assert.AnError)

	var written *v1.ModelRegistryStatus

	mockStorage.On("UpdateModelRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
		obj, ok := args.Get(1).(*v1.ModelRegistry)
		if !ok {
			t.Fatalf("unexpected update payload %T", args.Get(1))
		}

		written = obj.Status
	}).Return(nil)

	c := newCheckedAtController(t, mockStorage, mockRegistry, now)

	failed := &v1.ModelRegistry{
		ID:       1,
		Metadata: &v1.Metadata{Name: "test", Workspace: "default"},
		Status: &v1.ModelRegistryStatus{
			Phase:              v1.ModelRegistryPhaseFAILED,
			LastTransitionTime: failedAt,
			// Checked a moment ago, so nothing but the changed reason can be what
			// triggers the write.
			LastCheckedAt: now.Add(-time.Second).Format(time.RFC3339Nano),
			ErrorMessage:  "invalid Hugging Face API token",
		},
	}

	assert.Error(t, c.sync(failed))

	if assert.NotNil(t, written) {
		assert.Equal(t, v1.ModelRegistryPhaseFAILED, written.Phase)
		assert.NotEqual(t, "invalid Hugging Face API token", written.ErrorMessage)
		assert.NotEmpty(t, written.ErrorMessage)
		assert.Equal(t, failedAt, written.LastTransitionTime,
			"still failing is not a new transition")
	}

	mockStorage.AssertExpectations(t)
	mockRegistry.AssertExpectations(t)
}

// Deleting a registry says nothing about whether it was answering, so the
// recorded check time is left where it was rather than moved.
func TestModelRegistryController_DeletionDoesNotCountAsACheck(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	checkedAt := now.Add(-30 * time.Second).Format(time.RFC3339Nano)

	mockStorage := &storagemocks.MockStorage{}
	mockRegistry := &modelregistrymocks.MockModelRegistry{}
	mockRegistry.On("Disconnect").Return(nil)

	var written *v1.ModelRegistryStatus

	mockStorage.On("UpdateModelRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
		obj, ok := args.Get(1).(*v1.ModelRegistry)
		if !ok {
			t.Fatalf("unexpected update payload %T", args.Get(1))
		}

		written = obj.Status
	}).Return(nil)

	c := newCheckedAtController(t, mockStorage, mockRegistry, now)

	deleting := &v1.ModelRegistry{
		ID: 1,
		Metadata: &v1.Metadata{
			Name:              "test",
			Workspace:         "default",
			DeletionTimestamp: now.Format(time.RFC3339Nano),
		},
		Status: &v1.ModelRegistryStatus{
			Phase:         v1.ModelRegistryPhaseCONNECTED,
			LastCheckedAt: checkedAt,
			Stats:         &v1.ModelRegistryStats{ModelCount: 3},
		},
	}

	assert.NoError(t, c.sync(deleting))

	if assert.NotNil(t, written) {
		assert.Equal(t, v1.ModelRegistryPhaseDELETED, written.Phase)
		assert.Equal(t, checkedAt, written.LastCheckedAt)
		assert.Equal(t, 3, written.Stats.ModelCount)
	}

	mockStorage.AssertExpectations(t)
	mockRegistry.AssertExpectations(t)
}
