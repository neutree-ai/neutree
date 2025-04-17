package controllers

import (
	"strconv"
	"testing"
	"time"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"k8s.io/client-go/util/workqueue"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

// newTestEngineController is a helper to create a EngineController with mocked storage for testing.
func newTestEngineController(storage *storagemocks.MockStorage) *EngineController {
	c, _ := NewEngineController(&EngineControllerOption{
		Storage: storage,
		Workers: 1,
	})
	// Use a predictable queue for testing.
	c.baseController.queue = workqueue.NewRateLimitingQueueWithConfig(workqueue.DefaultControllerRateLimiter(), workqueue.RateLimitingQueueConfig{Name: "engine-test"})
	return c
}

// testEngine is a helper to create a basic Engine object for tests.
func testEngine(id int, phase v1.EnginePhase) *v1.Engine {
	engine := &v1.Engine{
		ID: id,
		Metadata: &v1.Metadata{
			Name: "test-engine-" + strconv.Itoa(id),
		},
		Spec: &v1.EngineSpec{},
	}
	if phase != "" { // Only set status if phase is provided.
		engine.Status = &v1.EngineStatus{Phase: phase}
	}
	return engine
}

// testEngineWithDeletionTimestamp is a helper to create a Engine object marked for deletion.
func testEngineWithDeletionTimestamp(id int, phase v1.EnginePhase) *v1.Engine {
	engine := testEngine(id, phase)
	engine.Metadata.DeletionTimestamp = time.Now().Format(time.RFC3339Nano)
	return engine
}

// --- Tests for the 'sync' method ---

func TestEngineController_Sync_Deletion(t *testing.T) {
	engineID := 1
	engineIDStr := strconv.Itoa(engineID)

	tests := []struct {
		name      string
		input     *v1.Engine
		mockSetup func(*storagemocks.MockStorage)
		wantErr   bool
	}{
		{
			name:  "Deleting (Phase=DELETED) -> Deleted (DB delete success)",
			input: testEngineWithDeletionTimestamp(engineID, v1.EnginePhaseDeleted),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("DeleteEngine", engineIDStr).Return(nil).Once()
			},
			wantErr: false,
		},
		{
			name:  "Deleting (Phase=FAILED) -> Deleted (DB delete success)",
			input: testEngineWithDeletionTimestamp(engineID, v1.EnginePhaseFailed),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("DeleteEngine", engineIDStr).Return(nil).Once()
			},
			wantErr: false,
		},
		{
			name:  "Deleting (Phase=DELETED) -> Error (DB delete failed)",
			input: testEngineWithDeletionTimestamp(engineID, v1.EnginePhaseDeleted),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("DeleteEngine", engineIDStr).Return(assert.AnError).Once()
			},
			wantErr: true,
		},
		{
			name:  "Deleting (Phase=CREATED) -> Set Phase=DELETED (Update success)",
			input: testEngineWithDeletionTimestamp(engineID, v1.EnginePhaseCreated),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("UpdateEngine", engineIDStr, mock.MatchedBy(func(r *v1.Engine) bool {
					return r.Status != nil && r.Status.Phase == v1.EnginePhaseDeleted && r.Status.ErrorMessage == ""
				})).Return(nil).Once()
			},
			wantErr: false,
		},
		{
			name:  "Deleting (Phase=PENDING) -> Set Phase=DELETED (Update failed)",
			input: testEngineWithDeletionTimestamp(engineID, v1.EnginePhasePending),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("UpdateEngine", engineIDStr, mock.MatchedBy(func(r *v1.Engine) bool {
					return r.Status != nil && r.Status.Phase == v1.EnginePhaseDeleted
				})).Return(assert.AnError).Once()
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			tt.mockSetup(mockStorage)
			c := newTestEngineController(mockStorage)

			err := c.sync(tt.input) // Test sync directly.

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			mockStorage.AssertExpectations(t)
		})
	}
}

func TestEngineController_Sync_CreateOrUpdate(t *testing.T) {
	engineID := 1
	engineIDStr := strconv.Itoa(engineID)

	tests := []struct {
		name      string
		input     *v1.Engine
		mockSetup func(*storagemocks.MockStorage)
		wantErr   bool
	}{
		{
			name:  "No Status -> Set Phase=CREATED (Update success)",
			input: testEngine(engineID, ""),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("UpdateEngine", engineIDStr, mock.MatchedBy(func(r *v1.Engine) bool {
					return r.Status != nil && r.Status.Phase == v1.EnginePhaseCreated && r.Status.ErrorMessage == ""
				})).Return(nil).Once()
			},
			wantErr: false,
		},
		{
			name:  "Phase=PENDING -> Set Phase=CREATED (Update success)",
			input: testEngine(engineID, v1.EnginePhasePending),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("UpdateEngine", engineIDStr, mock.MatchedBy(func(r *v1.Engine) bool {
					return r.Status != nil && r.Status.Phase == v1.EnginePhaseCreated && r.Status.ErrorMessage == ""
				})).Return(nil).Once()
			},
			wantErr: false,
		},
		{
			name:  "Phase=PENDING -> Set Phase=CREATED (Update failed)",
			input: testEngine(engineID, v1.EnginePhasePending),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("UpdateEngine", engineIDStr, mock.MatchedBy(func(r *v1.Engine) bool {
					return r.Status != nil && r.Status.Phase == v1.EnginePhaseCreated
				})).Return(assert.AnError).Once()
			},
			wantErr: true,
		},
		{
			name:  "Phase=CREATED -> No Change",
			input: testEngine(engineID, v1.EnginePhaseCreated),
			mockSetup: func(s *storagemocks.MockStorage) {
				// Expect no calls to UpdateEngine or DeleteEngine.
			},
			wantErr: false,
		},
		{
			name:  "Phase=DELETED (no deletionTimestamp) -> No Change",
			input: testEngine(engineID, v1.EnginePhaseDeleted),
			mockSetup: func(s *storagemocks.MockStorage) {
				// Expect no calls to UpdateEngine or DeleteEngine.
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			tt.mockSetup(mockStorage)
			c := newTestEngineController(mockStorage)

			err := c.sync(tt.input) // Test sync directly.

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			mockStorage.AssertExpectations(t)
		})
	}
}

// --- Test for ListKeys ---

func TestEngineController_ListKeys(t *testing.T) {
	tests := []struct {
		name      string
		mockSetup func(*storagemocks.MockStorage)
		wantKeys  []interface{}
		wantErr   bool
	}{
		{
			name: "List success",
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("ListEngine", storage.ListOption{}).Return([]v1.Engine{
					{ID: 1}, {ID: 5}, {ID: 10},
				}, nil).Once()
			},
			wantKeys: []interface{}{1, 5, 10},
			wantErr:  false,
		},
		{
			name: "List returns empty",
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("ListEngine", storage.ListOption{}).Return([]v1.Engine{}, nil).Once()
			},
			wantKeys: []interface{}{},
			wantErr:  false,
		},
		{
			name: "List returns error",
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("ListEngine", storage.ListOption{}).Return(nil, assert.AnError).Once()
			},
			wantKeys: nil,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			tt.mockSetup(mockStorage)
			c := newTestEngineController(mockStorage)

			keys, err := c.ListKeys()

			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, keys)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.wantKeys, keys)
			}
			mockStorage.AssertExpectations(t)
		})
	}
}

// --- Test for Reconcile ---

func TestEngineController_Reconcile(t *testing.T) {
	engineID := 1
	engineIDStr := strconv.Itoa(engineID)

	// mockSyncHandler provides a controllable sync function for Reconcile tests.
	mockSyncHandler := func(obj *v1.Engine) error {
		// Check for a condition to simulate failure.
		if obj != nil && obj.Metadata != nil && obj.Metadata.Name == "sync-should-fail" {
			return errors.New("mock sync failed")
		}
		// Simulate successful sync.
		return nil
	}

	tests := []struct {
		name          string
		inputKey      interface{}
		mockSetup     func(*storagemocks.MockStorage)
		useMockSync   bool  // Flag to indicate if the mock syncHandler should be used.
		expectedError error // Expected contained error string for specific checks.
		wantErr       bool
	}{
		{
			name:     "Reconcile success (real sync, no status change)", // Test scenario using default sync handler.
			inputKey: engineID,
			mockSetup: func(s *storagemocks.MockStorage) {
				// GetEngine succeeds, engine is already in the desired state.
				s.On("GetEngine", engineIDStr).Return(testEngine(engineID, v1.EnginePhaseCreated), nil).Once()
				// The real 'sync' method expects no further storage calls here.
			},
			useMockSync: false, // Use the default c.sync via syncHandler.
			wantErr:     false,
		},
		{
			name:     "Reconcile success (real sync, status updated)", // Test scenario using default sync handler.
			inputKey: engineID,
			mockSetup: func(s *storagemocks.MockStorage) {
				// GetEngine succeeds, engine needs status update.
				s.On("GetEngine", engineIDStr).Return(testEngine(engineID, v1.EnginePhasePending), nil).Once()
				// The real 'sync' method expects UpdateEngine to be called.
				s.On("UpdateEngine", engineIDStr, mock.MatchedBy(func(r *v1.Engine) bool {
					return r.Status != nil && r.Status.Phase == v1.EnginePhaseCreated
				})).Return(nil).Once()
			},
			useMockSync: false, // Use the default c.sync via syncHandler.
			wantErr:     false,
		},
		{
			name:     "Reconcile success (mock sync)", // Test Reconcile isolation using mock handler.
			inputKey: engineID,
			mockSetup: func(s *storagemocks.MockStorage) {
				// GetEngine succeeds.
				s.On("GetEngine", engineIDStr).Return(testEngine(engineID, v1.EnginePhaseCreated), nil).Once()
				// No further storage calls expected by Reconcile before calling syncHandler.
			},
			useMockSync: true, // Override with mockSyncHandler.
			wantErr:     false,
		},
		{
			name:     "Invalid key type",
			inputKey: "not-an-int",
			mockSetup: func(s *storagemocks.MockStorage) {
				// No storage calls expected.
			},
			useMockSync:   false, // Fails before sync handler.
			wantErr:       true,
			expectedError: errors.New("failed to assert key to engineID"),
		},
		{
			name:     "GetEngine returns error",
			inputKey: engineID,
			mockSetup: func(s *storagemocks.MockStorage) {
				// Mock GetEngine to return an error.
				s.On("GetEngine", engineIDStr).Return(nil, assert.AnError).Once()
			},
			useMockSync: false, // Fails before sync handler.
			wantErr:     true,  // Expect error from GetEngine to be propagated.
		},
		{
			name:     "Sync handler returns error (mock sync)",
			inputKey: engineID,
			mockSetup: func(s *storagemocks.MockStorage) {
				// GetEngine succeeds, providing the engine that triggers mock failure.
				engine := testEngine(engineID, v1.EnginePhaseCreated)
				engine.Metadata.Name = "sync-should-fail" // Condition for mockSyncHandler failure.
				s.On("GetEngine", engineIDStr).Return(engine, nil).Once()
			},
			useMockSync: true, // Use the mock handler.
			wantErr:     true, // Expect error from mock sync handler to be propagated.
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			if tt.mockSetup != nil {
				tt.mockSetup(mockStorage)
			}

			// Create controller using the helper.
			c := newTestEngineController(mockStorage)

			// Override syncHandler if the test case requires the mock.
			if tt.useMockSync {
				c.syncHandler = mockSyncHandler
			}

			// Directly call the Reconcile method.
			err := c.Reconcile(tt.inputKey)

			// Assertions.
			if tt.wantErr {
				assert.Error(t, err)
				if tt.expectedError != nil {
					// Use Contains for checking wrapped errors.
					assert.Contains(t, err.Error(), tt.expectedError.Error())
				}
			} else {
				assert.NoError(t, err)
			}
			// Verify mock expectations.
			mockStorage.AssertExpectations(t)
		})
	}
}
