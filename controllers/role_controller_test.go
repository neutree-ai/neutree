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

// newTestRoleController is a helper to create a RoleController with mocked storage for testing.
func newTestRoleController(storage *storagemocks.MockStorage) *RoleController {
	c, _ := NewRoleController(&RoleControllerOption{
		Storage: storage,
		Workers: 1,
	})
	// Use a predictable queue for testing.
	c.baseController.queue = workqueue.NewRateLimitingQueueWithConfig(workqueue.DefaultControllerRateLimiter(), workqueue.RateLimitingQueueConfig{Name: "role-test"})
	return c
}

// testRole is a helper to create a basic Role object for tests.
func testRole(id int, phase v1.RolePhase) *v1.Role {
	role := &v1.Role{
		ID: id,
		Metadata: &v1.Metadata{
			Name: "test-role-" + strconv.Itoa(id),
		},
		Spec: &v1.RoleSpec{},
	}
	if phase != "" { // Only set status if phase is provided.
		role.Status = &v1.RoleStatus{Phase: phase}
	}
	return role
}

// testRoleWithDeletionTimestamp is a helper to create a Role object marked for deletion.
func testRoleWithDeletionTimestamp(id int, phase v1.RolePhase) *v1.Role {
	role := testRole(id, phase)
	role.Metadata.DeletionTimestamp = time.Now().Format(time.RFC3339Nano)
	return role
}

// --- Tests for the 'sync' method ---

func TestRoleController_Sync_Deletion(t *testing.T) {
	roleID := 1
	roleIDStr := strconv.Itoa(roleID)

	tests := []struct {
		name      string
		input     *v1.Role
		mockSetup func(*storagemocks.MockStorage)
		wantErr   bool
	}{
		{
			name:  "Deleting (Phase=DELETED) -> Deleted (DB delete success)",
			input: testRoleWithDeletionTimestamp(roleID, v1.RolePhaseDELETED),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("DeleteRole", roleIDStr).Return(nil).Once()
			},
			wantErr: false,
		},
		{
			name:  "Deleting (Phase=DELETED) -> Error (DB delete failed)",
			input: testRoleWithDeletionTimestamp(roleID, v1.RolePhaseDELETED),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("DeleteRole", roleIDStr).Return(assert.AnError).Once()
			},
			wantErr: true,
		},
		{
			name:  "Deleting (Phase=CREATED) -> Set Phase=DELETED (Update success)",
			input: testRoleWithDeletionTimestamp(roleID, v1.RolePhaseCREATED),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("UpdateRole", roleIDStr, mock.MatchedBy(func(r *v1.Role) bool {
					return r.Status != nil && r.Status.Phase == v1.RolePhaseDELETED && r.Status.ErrorMessage == ""
				})).Return(nil).Once()
			},
			wantErr: false,
		},
		{
			name:  "Deleting (Phase=PENDING) -> Set Phase=DELETED (Update failed)",
			input: testRoleWithDeletionTimestamp(roleID, v1.RolePhasePENDING),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("UpdateRole", roleIDStr, mock.MatchedBy(func(r *v1.Role) bool {
					return r.Status != nil && r.Status.Phase == v1.RolePhaseDELETED
				})).Return(assert.AnError).Once()
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			tt.mockSetup(mockStorage)
			c := newTestRoleController(mockStorage)

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

func TestRoleController_Sync_CreateOrUpdate(t *testing.T) {
	roleID := 1
	roleIDStr := strconv.Itoa(roleID)

	tests := []struct {
		name      string
		input     *v1.Role
		mockSetup func(*storagemocks.MockStorage)
		wantErr   bool
	}{
		{
			name:  "No Status -> Set Phase=CREATED (Update success)",
			input: testRole(roleID, ""),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("UpdateRole", roleIDStr, mock.MatchedBy(func(r *v1.Role) bool {
					return r.Status != nil && r.Status.Phase == v1.RolePhaseCREATED && r.Status.ErrorMessage == ""
				})).Return(nil).Once()
			},
			wantErr: false,
		},
		{
			name:  "Phase=PENDING -> Set Phase=CREATED (Update success)",
			input: testRole(roleID, v1.RolePhasePENDING),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("UpdateRole", roleIDStr, mock.MatchedBy(func(r *v1.Role) bool {
					return r.Status != nil && r.Status.Phase == v1.RolePhaseCREATED && r.Status.ErrorMessage == ""
				})).Return(nil).Once()
			},
			wantErr: false,
		},
		{
			name:  "Phase=PENDING -> Set Phase=CREATED (Update failed)",
			input: testRole(roleID, v1.RolePhasePENDING),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("UpdateRole", roleIDStr, mock.MatchedBy(func(r *v1.Role) bool {
					return r.Status != nil && r.Status.Phase == v1.RolePhaseCREATED
				})).Return(assert.AnError).Once()
			},
			wantErr: true,
		},
		{
			name:  "Phase=CREATED -> No Change",
			input: testRole(roleID, v1.RolePhaseCREATED),
			mockSetup: func(s *storagemocks.MockStorage) {
				// Expect no calls to UpdateRole or DeleteRole.
			},
			wantErr: false,
		},
		{
			name:  "Phase=DELETED (no deletionTimestamp) -> No Change",
			input: testRole(roleID, v1.RolePhaseDELETED),
			mockSetup: func(s *storagemocks.MockStorage) {
				// Expect no calls to UpdateRole or DeleteRole.
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			tt.mockSetup(mockStorage)
			c := newTestRoleController(mockStorage)

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

func TestRoleController_ListKeys(t *testing.T) {
	tests := []struct {
		name      string
		mockSetup func(*storagemocks.MockStorage)
		wantKeys  []interface{}
		wantErr   bool
	}{
		{
			name: "List success",
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("ListRole", storage.ListOption{}).Return([]v1.Role{
					{ID: 1}, {ID: 5}, {ID: 10},
				}, nil).Once()
			},
			wantKeys: []interface{}{1, 5, 10},
			wantErr:  false,
		},
		{
			name: "List returns empty",
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("ListRole", storage.ListOption{}).Return([]v1.Role{}, nil).Once()
			},
			wantKeys: []interface{}{},
			wantErr:  false,
		},
		{
			name: "List returns error",
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("ListRole", storage.ListOption{}).Return(nil, assert.AnError).Once()
			},
			wantKeys: nil,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			tt.mockSetup(mockStorage)
			c := newTestRoleController(mockStorage)

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

func TestRoleController_Reconcile(t *testing.T) {
	roleID := 1
	roleIDStr := strconv.Itoa(roleID)

	// mockSyncHandler provides a controllable sync function for Reconcile tests.
	mockSyncHandler := func(obj *v1.Role) error {
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
			inputKey: roleID,
			mockSetup: func(s *storagemocks.MockStorage) {
				// GetRole succeeds, role is already in the desired state.
				s.On("GetRole", roleIDStr).Return(testRole(roleID, v1.RolePhaseCREATED), nil).Once()
				// The real 'sync' method expects no further storage calls here.
			},
			useMockSync: false, // Use the default c.sync via syncHandler.
			wantErr:     false,
		},
		{
			name:     "Reconcile success (real sync, status updated)", // Test scenario using default sync handler.
			inputKey: roleID,
			mockSetup: func(s *storagemocks.MockStorage) {
				// GetRole succeeds, role needs status update.
				s.On("GetRole", roleIDStr).Return(testRole(roleID, v1.RolePhasePENDING), nil).Once()
				// The real 'sync' method expects UpdateRole to be called.
				s.On("UpdateRole", roleIDStr, mock.MatchedBy(func(r *v1.Role) bool {
					return r.Status != nil && r.Status.Phase == v1.RolePhaseCREATED
				})).Return(nil).Once()
			},
			useMockSync: false, // Use the default c.sync via syncHandler.
			wantErr:     false,
		},
		{
			name:     "Reconcile success (mock sync)", // Test Reconcile isolation using mock handler.
			inputKey: roleID,
			mockSetup: func(s *storagemocks.MockStorage) {
				// GetRole succeeds.
				s.On("GetRole", roleIDStr).Return(testRole(roleID, v1.RolePhaseCREATED), nil).Once()
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
			expectedError: errors.New("failed to assert key to roleID"),
		},
		{
			name:     "GetRole returns error",
			inputKey: roleID,
			mockSetup: func(s *storagemocks.MockStorage) {
				// Mock GetRole to return an error.
				s.On("GetRole", roleIDStr).Return(nil, assert.AnError).Once()
			},
			useMockSync: false, // Fails before sync handler.
			wantErr:     true,  // Expect error from GetRole to be propagated.
		},
		{
			name:     "Sync handler returns error (mock sync)",
			inputKey: roleID,
			mockSetup: func(s *storagemocks.MockStorage) {
				// GetRole succeeds, providing the role that triggers mock failure.
				role := testRole(roleID, v1.RolePhaseCREATED)
				role.Metadata.Name = "sync-should-fail" // Condition for mockSyncHandler failure.
				s.On("GetRole", roleIDStr).Return(role, nil).Once()
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
			c := newTestRoleController(mockStorage)

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
