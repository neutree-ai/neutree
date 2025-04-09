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

// newTestRoleAssignmentController is a helper to create a RoleAssignmentController with mocked storage for testing.
func newTestRoleAssignmentController(storage *storagemocks.MockStorage) *RoleAssignmentController {
	c, _ := NewRoleAssignmentController(&RoleAssignmentControllerOption{
		Storage: storage,
		Workers: 1,
	})
	// Use a predictable queue for testing.
	c.baseController.queue = workqueue.NewRateLimitingQueueWithConfig(workqueue.DefaultControllerRateLimiter(), workqueue.RateLimitingQueueConfig{Name: "roleassignment-test"})
	return c
}

// testRoleAssignment is a helper to create a basic RoleAssignment object for tests.
func testRoleAssignment(id int, phase v1.RoleAssignmentPhase) *v1.RoleAssignment {
	ra := &v1.RoleAssignment{
		ID: id,
		Metadata: &v1.Metadata{
			Name: "test-ra-" + strconv.Itoa(id),
		},
		Spec: &v1.RoleAssignmentSpec{
			UserID: "user-" + strconv.Itoa(id),
			Role:   "role-viewer",
		},
	}
	if phase != "" { // Only set status if phase is provided.
		ra.Status = &v1.RoleAssignmentStatus{Phase: phase}
	}
	return ra
}

// testRoleAssignmentWithDeletionTimestamp is a helper to create a RoleAssignment object marked for deletion.
func testRoleAssignmentWithDeletionTimestamp(id int, phase v1.RoleAssignmentPhase) *v1.RoleAssignment {
	ra := testRoleAssignment(id, phase)
	ra.Metadata.DeletionTimestamp = time.Now().Format(time.RFC3339Nano)
	return ra
}

// --- Tests for the 'sync' method ---

func TestRoleAssignmentController_Sync_Deletion(t *testing.T) {
	raID := 1
	raIDStr := strconv.Itoa(raID)

	tests := []struct {
		name      string
		input     *v1.RoleAssignment
		mockSetup func(*storagemocks.MockStorage)
		wantErr   bool
	}{
		{
			name:  "Deleting (Phase=DELETED) -> Deleted (DB delete success)",
			input: testRoleAssignmentWithDeletionTimestamp(raID, v1.RoleAssignmentPhaseDELETED),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("DeleteRoleAssignment", raIDStr).Return(nil).Once()
			},
			wantErr: false,
		},
		{
			name:  "Deleting (Phase=DELETED) -> Error (DB delete failed)",
			input: testRoleAssignmentWithDeletionTimestamp(raID, v1.RoleAssignmentPhaseDELETED),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("DeleteRoleAssignment", raIDStr).Return(assert.AnError).Once()
			},
			wantErr: true,
		},
		{
			name:  "Deleting (Phase=DELETED) -> Already Deleted (DB delete returns NotFound)",
			input: testRoleAssignmentWithDeletionTimestamp(raID, v1.RoleAssignmentPhaseDELETED),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("DeleteRoleAssignment", raIDStr).Return(storage.ErrResourceNotFound).Once()
			},
			wantErr: false, // NotFound is not an error in this case
		},
		{
			name:  "Deleting (Phase=CREATED) -> Set Phase=DELETED (Update success)",
			input: testRoleAssignmentWithDeletionTimestamp(raID, v1.RoleAssignmentPhaseCREATED),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("UpdateRoleAssignment", raIDStr, mock.MatchedBy(func(r *v1.RoleAssignment) bool {
					return r.Status != nil && r.Status.Phase == v1.RoleAssignmentPhaseDELETED && r.Status.ErrorMessage == "" && r.Spec == nil && r.Metadata == nil // Ensure only status is updated
				})).Return(nil).Once()
			},
			wantErr: false,
		},
		{
			name:  "Deleting (Phase=PENDING) -> Set Phase=DELETED (Update failed)",
			input: testRoleAssignmentWithDeletionTimestamp(raID, v1.RoleAssignmentPhasePENDING),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("UpdateRoleAssignment", raIDStr, mock.MatchedBy(func(r *v1.RoleAssignment) bool {
					return r.Status != nil && r.Status.Phase == v1.RoleAssignmentPhaseDELETED
				})).Return(assert.AnError).Once()
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			tt.mockSetup(mockStorage)
			c := newTestRoleAssignmentController(mockStorage)

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

func TestRoleAssignmentController_Sync_CreateOrUpdate(t *testing.T) {
	raID := 1
	raIDStr := strconv.Itoa(raID)

	tests := []struct {
		name      string
		input     *v1.RoleAssignment
		mockSetup func(*storagemocks.MockStorage)
		wantErr   bool
	}{
		{
			name:  "No Status -> Set Phase=CREATED (Update success)",
			input: testRoleAssignment(raID, ""),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("UpdateRoleAssignment", raIDStr, mock.MatchedBy(func(r *v1.RoleAssignment) bool {
					return r.Status != nil && r.Status.Phase == v1.RoleAssignmentPhaseCREATED && r.Status.ErrorMessage == "" && r.Spec == nil && r.Metadata == nil
				})).Return(nil).Once()
			},
			wantErr: false,
		},
		{
			name:  "Phase=PENDING -> Set Phase=CREATED (Update success)",
			input: testRoleAssignment(raID, v1.RoleAssignmentPhasePENDING),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("UpdateRoleAssignment", raIDStr, mock.MatchedBy(func(r *v1.RoleAssignment) bool {
					return r.Status != nil && r.Status.Phase == v1.RoleAssignmentPhaseCREATED && r.Status.ErrorMessage == "" && r.Spec == nil && r.Metadata == nil
				})).Return(nil).Once()
			},
			wantErr: false,
		},
		{
			name:  "Phase=PENDING -> Set Phase=CREATED (Update failed)",
			input: testRoleAssignment(raID, v1.RoleAssignmentPhasePENDING),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("UpdateRoleAssignment", raIDStr, mock.MatchedBy(func(r *v1.RoleAssignment) bool {
					return r.Status != nil && r.Status.Phase == v1.RoleAssignmentPhaseCREATED
				})).Return(assert.AnError).Once()
			},
			wantErr: true,
		},
		{
			name:  "Phase=CREATED -> No Change",
			input: testRoleAssignment(raID, v1.RoleAssignmentPhaseCREATED),
			mockSetup: func(s *storagemocks.MockStorage) {
				// Expect no calls to UpdateRoleAssignment or DeleteRoleAssignment.
			},
			wantErr: false,
		},
		{
			name:  "Phase=DELETED (no deletionTimestamp) -> No Change",
			input: testRoleAssignment(raID, v1.RoleAssignmentPhaseDELETED),
			mockSetup: func(s *storagemocks.MockStorage) {
				// Expect no calls to UpdateRoleAssignment or DeleteRoleAssignment.
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			tt.mockSetup(mockStorage)
			c := newTestRoleAssignmentController(mockStorage)

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

func TestRoleAssignmentController_ListKeys(t *testing.T) {
	tests := []struct {
		name      string
		mockSetup func(*storagemocks.MockStorage)
		wantKeys  []interface{}
		wantErr   bool
	}{
		{
			name: "List success",
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("ListRoleAssignment", storage.ListOption{}).Return([]v1.RoleAssignment{
					{ID: 1}, {ID: 5}, {ID: 10},
				}, nil).Once()
			},
			wantKeys: []interface{}{1, 5, 10},
			wantErr:  false,
		},
		{
			name: "List returns empty",
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("ListRoleAssignment", storage.ListOption{}).Return([]v1.RoleAssignment{}, nil).Once()
			},
			wantKeys: []interface{}{},
			wantErr:  false,
		},
		{
			name: "List returns error",
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("ListRoleAssignment", storage.ListOption{}).Return(nil, assert.AnError).Once()
			},
			wantKeys: nil,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			tt.mockSetup(mockStorage)
			c := newTestRoleAssignmentController(mockStorage)

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

func TestRoleAssignmentController_Reconcile(t *testing.T) {
	raID := 1
	raIDStr := strconv.Itoa(raID)

	// mockSyncHandler provides a controllable sync function for Reconcile tests.
	mockSyncHandler := func(obj *v1.RoleAssignment) error {
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
			inputKey: raID,
			mockSetup: func(s *storagemocks.MockStorage) {
				// GetRoleAssignment succeeds, role assignment is already in the desired state.
				s.On("GetRoleAssignment", raIDStr).Return(testRoleAssignment(raID, v1.RoleAssignmentPhaseCREATED), nil).Once()
				// The real 'sync' method expects no further storage calls here.
			},
			useMockSync: false, // Use the default c.sync via syncHandler.
			wantErr:     false,
		},
		{
			name:     "Reconcile success (real sync, status updated)", // Test scenario using default sync handler.
			inputKey: raID,
			mockSetup: func(s *storagemocks.MockStorage) {
				// GetRoleAssignment succeeds, role assignment needs status update.
				s.On("GetRoleAssignment", raIDStr).Return(testRoleAssignment(raID, v1.RoleAssignmentPhasePENDING), nil).Once()
				// The real 'sync' method expects UpdateRoleAssignment to be called.
				s.On("UpdateRoleAssignment", raIDStr, mock.MatchedBy(func(r *v1.RoleAssignment) bool {
					return r.Status != nil && r.Status.Phase == v1.RoleAssignmentPhaseCREATED
				})).Return(nil).Once()
			},
			useMockSync: false, // Use the default c.sync via syncHandler.
			wantErr:     false,
		},
		{
			name:     "Reconcile success (mock sync)", // Test Reconcile isolation using mock handler.
			inputKey: raID,
			mockSetup: func(s *storagemocks.MockStorage) {
				// GetRoleAssignment succeeds.
				s.On("GetRoleAssignment", raIDStr).Return(testRoleAssignment(raID, v1.RoleAssignmentPhaseCREATED), nil).Once()
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
			expectedError: errors.New("failed to assert key to roleAssignmentID"),
		},
		{
			name:     "GetRoleAssignment returns error",
			inputKey: raID,
			mockSetup: func(s *storagemocks.MockStorage) {
				// Mock GetRoleAssignment to return an error.
				s.On("GetRoleAssignment", raIDStr).Return(nil, assert.AnError).Once()
			},
			useMockSync: false, // Fails before sync handler.
			wantErr:     true,  // Expect error from GetRoleAssignment to be propagated.
		},
		{
			name:     "GetRoleAssignment returns NotFound",
			inputKey: raID,
			mockSetup: func(s *storagemocks.MockStorage) {
				// Mock GetRoleAssignment to return NotFound.
				s.On("GetRoleAssignment", raIDStr).Return(nil, storage.ErrResourceNotFound).Once()
			},
			useMockSync: false, // Fails before sync handler.
			wantErr:     false, // NotFound should be handled gracefully (logged and skipped).
		},
		{
			name:     "Sync handler returns error (mock sync)",
			inputKey: raID,
			mockSetup: func(s *storagemocks.MockStorage) {
				// GetRoleAssignment succeeds, providing the role assignment that triggers mock failure.
				ra := testRoleAssignment(raID, v1.RoleAssignmentPhaseCREATED)
				ra.Metadata.Name = "sync-should-fail" // Condition for mockSyncHandler failure.
				s.On("GetRoleAssignment", raIDStr).Return(ra, nil).Once()
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
			c := newTestRoleAssignmentController(mockStorage)

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
