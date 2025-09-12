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

// newTestRoleController is a helper to create a RoleController with mocked storage for testing.
func newTestRoleController(storage *storagemocks.MockStorage) *RoleController {
	c, _ := NewRoleController(&RoleControllerOption{
		Storage: storage,
	})

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

// --- Test for Reconcile ---

func TestRoleController_Reconcile(t *testing.T) {
	roleID := 1
	failedRoleID := 2
	roleIDStr := strconv.Itoa(roleID)

	// mockSyncHandler provides a controllable sync function for Reconcile tests.
	mockSyncHandler := func(obj *v1.Role) error {
		// Check for a condition to simulate failure.
		if obj != nil && obj.ID == failedRoleID {
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
			inputKey: testRole(roleID, v1.RolePhaseCREATED),
			mockSetup: func(s *storagemocks.MockStorage) {
				// The real 'sync' method expects no further storage calls here.
			},
			useMockSync: false, // Use the default c.sync via syncHandler.
			wantErr:     false,
		},
		{
			name:     "Reconcile success (real sync, status updated)", // Test scenario using default sync handler.
			inputKey: testRole(roleID, v1.RolePhasePENDING),
			mockSetup: func(s *storagemocks.MockStorage) {
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
			inputKey: testRole(roleID, v1.RolePhaseCREATED),
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
			expectedError: errors.New("failed to assert obj to *v1.Role"),
		},
		{
			name:     "Sync handler returns error (mock sync)",
			inputKey: testRole(failedRoleID, v1.RolePhaseCREATED),
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
