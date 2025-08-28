package controllers

import (
	"strconv"
	"testing"
	"time"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	v1 "github.com/neutree-ai/neutree/api/v1"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

// newTestEngineController is a helper to create a EngineController with mocked storage for testing.
func newTestEngineController(storage *storagemocks.MockStorage) *EngineController {
	c, _ := NewEngineController(&EngineControllerOption{
		Storage: storage,
	})
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

// --- Test for Reconcile ---

func TestEngineController_Reconcile(t *testing.T) {
	engineID := 1
	failedEngineID := 2
	engineIDStr := strconv.Itoa(engineID)

	// mockSyncHandler provides a controllable sync function for Reconcile tests.
	mockSyncHandler := func(obj *v1.Engine) error {
		// Check for a condition to simulate failure.
		if obj != nil && obj.ID == failedEngineID {
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
			inputKey: testEngine(engineID, v1.EnginePhaseCreated),
			mockSetup: func(s *storagemocks.MockStorage) {
			},
			useMockSync: false, // Use the default c.sync via syncHandler.
			wantErr:     false,
		},
		{
			name:     "Reconcile success (real sync, status updated)", // Test scenario using default sync handler.
			inputKey: testEngine(engineID, v1.EnginePhasePending),
			mockSetup: func(s *storagemocks.MockStorage) {
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
			inputKey: testEngine(engineID, v1.EnginePhaseCreated),
			mockSetup: func(s *storagemocks.MockStorage) {
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
			expectedError: errors.New("failed to assert obj to *v1.Engine"),
		},
		{
			name:     "Sync handler returns error (mock sync)",
			inputKey: testEngine(failedEngineID, v1.EnginePhaseCreated), // Use ID that triggers mock failure.
			mockSetup: func(s *storagemocks.MockStorage) {
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
