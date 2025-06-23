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
	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/neutree-ai/neutree/pkg/storage"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

// newTestWorkspaceController is a helper to create a WorkspaceController with mocked storage for testing.
func newTestWorkspaceController(storage *storagemocks.MockStorage) *WorkspaceController {
	c, _ := NewWorkspaceController(&WorkspaceControllerOption{
		Storage:            storage,
		Workers:            1,
		AcceleratorManager: accelerator.NewManager("127.0.0.1:3001"),
	})
	// Use a predictable queue for testing.
	c.baseController.queue = workqueue.NewRateLimitingQueueWithConfig(workqueue.DefaultControllerRateLimiter(), workqueue.RateLimitingQueueConfig{Name: "workspace-test"})
	return c
}

// testWorkspace is a helper to create a basic Workspace object for tests.
func testWorkspace(id int, phase v1.WorkspacePhase) *v1.Workspace {
	workspace := &v1.Workspace{
		ID: id,
		Metadata: &v1.Metadata{
			Name: "test-workspace-" + strconv.Itoa(id),
		},
		// Spec is not defined in the provided type, so omit it.
	}
	if phase != "" { // Only set status if phase is provided.
		workspace.Status = &v1.WorkspaceStatus{Phase: phase}
	}
	return workspace
}

// testWorkspaceWithDeletionTimestamp is a helper to create a Workspace object marked for deletion.
func testWorkspaceWithDeletionTimestamp(id int, phase v1.WorkspacePhase) *v1.Workspace {
	workspace := testWorkspace(id, phase)
	workspace.Metadata.DeletionTimestamp = time.Now().Format(time.RFC3339Nano)
	return workspace
}

// --- Tests for the 'sync' method ---

func TestWorkspaceController_Sync_Deletion(t *testing.T) {
	workspaceID := 1
	workspaceIDStr := strconv.Itoa(workspaceID)

	tests := []struct {
		name      string
		input     *v1.Workspace
		mockSetup func(*storagemocks.MockStorage)
		wantErr   bool
	}{
		{
			name:  "Deleting (Phase=DELETED) -> Deleted (DB delete success)",
			input: testWorkspaceWithDeletionTimestamp(workspaceID, v1.WorkspacePhaseDELETED),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("DeleteWorkspace", workspaceIDStr).Return(nil).Once()
			},
			wantErr: false,
		},
		{
			name:  "Deleting (Phase=DELETED) -> Error (DB delete failed)",
			input: testWorkspaceWithDeletionTimestamp(workspaceID, v1.WorkspacePhaseDELETED),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("DeleteWorkspace", workspaceIDStr).Return(assert.AnError).Once()
			},
			wantErr: true,
		},
		{
			name:  "Deleting (Phase=DELETED) -> Already Deleted (DB delete returns NotFound)",
			input: testWorkspaceWithDeletionTimestamp(workspaceID, v1.WorkspacePhaseDELETED),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("DeleteWorkspace", workspaceIDStr).Return(storage.ErrResourceNotFound).Once()
			},
			wantErr: false, // ErrResourceNotFound during delete is not an error for the controller
		},
		{
			name:  "Deleting (Phase=CREATED) -> Set Phase=DELETED (Update success)",
			input: testWorkspaceWithDeletionTimestamp(workspaceID, v1.WorkspacePhaseCREATED),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("ListEngine", mock.Anything).Return(nil, nil)
				s.On("UpdateWorkspace", workspaceIDStr, mock.MatchedBy(func(r *v1.Workspace) bool {
					return r.Status != nil && r.Status.Phase == v1.WorkspacePhaseDELETED && r.Status.ErrorMessage == ""
				})).Return(nil).Once()
			},
			wantErr: false,
		},
		{
			name:  "Deleting (Phase=PENDING) -> Set Phase=DELETED (Update failed)",
			input: testWorkspaceWithDeletionTimestamp(workspaceID, v1.WorkspacePhasePENDING),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("ListEngine", mock.Anything).Return(nil, nil)
				s.On("UpdateWorkspace", workspaceIDStr, mock.MatchedBy(func(r *v1.Workspace) bool {
					return r.Status != nil && r.Status.Phase == v1.WorkspacePhaseDELETED
				})).Return(assert.AnError).Once()
			},
			wantErr: true,
		},
		{
			name:  "Deleting (No Status) -> Set Phase=DELETED (Update success)",
			input: testWorkspaceWithDeletionTimestamp(workspaceID, ""), // No initial status
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("ListEngine", mock.Anything).Return(nil, nil)
				s.On("UpdateWorkspace", workspaceIDStr, mock.MatchedBy(func(r *v1.Workspace) bool {
					return r.Status != nil && r.Status.Phase == v1.WorkspacePhaseDELETED && r.Status.ErrorMessage == ""
				})).Return(nil).Once()
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			tt.mockSetup(mockStorage)
			c := newTestWorkspaceController(mockStorage)

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

func TestWorkspaceController_Sync_CreateOrUpdate(t *testing.T) {
	workspaceID := 1
	workspaceIDStr := strconv.Itoa(workspaceID)

	tests := []struct {
		name      string
		input     *v1.Workspace
		mockSetup func(*storagemocks.MockStorage)
		wantErr   bool
	}{
		{
			name:  "No Status -> Set Phase=CREATED (Update success)",
			input: testWorkspace(workspaceID, ""),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("UpdateWorkspace", workspaceIDStr, mock.MatchedBy(func(r *v1.Workspace) bool {
					return r.Status != nil && r.Status.Phase == v1.WorkspacePhaseCREATED && r.Status.ErrorMessage == ""
				})).Return(nil).Once()
			},
			wantErr: false,
		},
		{
			name:  "Phase=PENDING -> Set Phase=CREATED (Update success)",
			input: testWorkspace(workspaceID, v1.WorkspacePhasePENDING),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("UpdateWorkspace", workspaceIDStr, mock.MatchedBy(func(r *v1.Workspace) bool {
					return r.Status != nil && r.Status.Phase == v1.WorkspacePhaseCREATED && r.Status.ErrorMessage == ""
				})).Return(nil).Once()
			},
			wantErr: false,
		},
		{
			name:  "Phase=PENDING -> Set Phase=CREATED (Update failed)",
			input: testWorkspace(workspaceID, v1.WorkspacePhasePENDING),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("UpdateWorkspace", workspaceIDStr, mock.MatchedBy(func(r *v1.Workspace) bool {
					return r.Status != nil && r.Status.Phase == v1.WorkspacePhaseCREATED
				})).Return(assert.AnError).Once()
			},
			wantErr: true,
		},
		{
			name:  "Phase=CREATED -> No Change",
			input: testWorkspace(workspaceID, v1.WorkspacePhaseCREATED),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("ListEngine", mock.Anything).Return(nil, nil)
				s.On("CreateEngine", mock.Anything).Return(nil)
			},
			wantErr: false,
		},
		{
			name:  "Phase=DELETED (no deletionTimestamp) -> No Change",
			input: testWorkspace(workspaceID, v1.WorkspacePhaseDELETED),
			mockSetup: func(s *storagemocks.MockStorage) {
				// Expect no calls to UpdateWorkspace or DeleteWorkspace.
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			tt.mockSetup(mockStorage)
			c := newTestWorkspaceController(mockStorage)

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

func TestWorkspaceController_ListKeys(t *testing.T) {
	tests := []struct {
		name      string
		mockSetup func(*storagemocks.MockStorage)
		wantKeys  []interface{}
		wantErr   bool
	}{
		{
			name: "List success",
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("ListWorkspace", storage.ListOption{}).Return([]v1.Workspace{
					{ID: 1}, {ID: 5}, {ID: 10},
				}, nil).Once()
			},
			wantKeys: []interface{}{1, 5, 10},
			wantErr:  false,
		},
		{
			name: "List returns empty",
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("ListWorkspace", storage.ListOption{}).Return([]v1.Workspace{}, nil).Once()
			},
			wantKeys: []interface{}{},
			wantErr:  false,
		},
		{
			name: "List returns error",
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("ListWorkspace", storage.ListOption{}).Return(nil, assert.AnError).Once()
			},
			wantKeys: nil,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			tt.mockSetup(mockStorage)
			c := newTestWorkspaceController(mockStorage)

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

func TestWorkspaceController_Reconcile(t *testing.T) {
	workspaceID := 1
	workspaceIDStr := strconv.Itoa(workspaceID)

	// mockSyncHandler provides a controllable sync function for Reconcile tests.
	mockSyncHandler := func(obj *v1.Workspace) error {
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
			inputKey: workspaceID,
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("ListEngine", mock.Anything).Return(nil, nil)
				s.On("CreateEngine", mock.Anything).Return(nil)
				// GetWorkspace succeeds, workspace is already in the desired state.
				s.On("GetWorkspace", workspaceIDStr).Return(testWorkspace(workspaceID, v1.WorkspacePhaseCREATED), nil).Once()
				// The real 'sync' method expects no further storage calls here.
			},
			useMockSync: false, // Use the default c.sync via syncHandler.
			wantErr:     false,
		},
		{
			name:     "Reconcile success (real sync, status updated)", // Test scenario using default sync handler.
			inputKey: workspaceID,
			mockSetup: func(s *storagemocks.MockStorage) {
				// GetWorkspace succeeds, workspace needs status update.
				s.On("GetWorkspace", workspaceIDStr).Return(testWorkspace(workspaceID, v1.WorkspacePhasePENDING), nil).Once()
				// The real 'sync' method expects UpdateWorkspace to be called.
				s.On("UpdateWorkspace", workspaceIDStr, mock.MatchedBy(func(r *v1.Workspace) bool {
					return r.Status != nil && r.Status.Phase == v1.WorkspacePhaseCREATED
				})).Return(nil).Once()
			},
			useMockSync: false, // Use the default c.sync via syncHandler.
			wantErr:     false,
		},
		{
			name:     "Reconcile success (mock sync)", // Test Reconcile isolation using mock handler.
			inputKey: workspaceID,
			mockSetup: func(s *storagemocks.MockStorage) {
				// GetWorkspace succeeds.
				s.On("GetWorkspace", workspaceIDStr).Return(testWorkspace(workspaceID, v1.WorkspacePhaseCREATED), nil).Once()
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
			expectedError: errors.New("failed to assert key to workspaceID"),
		},
		{
			name:     "GetWorkspace returns error",
			inputKey: workspaceID,
			mockSetup: func(s *storagemocks.MockStorage) {
				// Mock GetWorkspace to return an error.
				s.On("GetWorkspace", workspaceIDStr).Return(nil, assert.AnError).Once()
			},
			useMockSync: false, // Fails before sync handler.
			wantErr:     true,  // Expect error from GetWorkspace to be propagated.
		},
		{
			name:     "GetWorkspace returns ErrResourceNotFound", // Specific check for NotFound
			inputKey: workspaceID,
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("GetWorkspace", workspaceIDStr).Return(nil, storage.ErrResourceNotFound).Once()
			},
			useMockSync:   false,
			wantErr:       true, // Reconcile propagates the error
			expectedError: storage.ErrResourceNotFound,
		},
		{
			name:     "Sync handler returns error (mock sync)",
			inputKey: workspaceID,
			mockSetup: func(s *storagemocks.MockStorage) {
				// GetWorkspace succeeds, providing the workspace that triggers mock failure.
				workspace := testWorkspace(workspaceID, v1.WorkspacePhaseCREATED)
				workspace.Metadata.Name = "sync-should-fail" // Condition for mockSyncHandler failure.
				s.On("GetWorkspace", workspaceIDStr).Return(workspace, nil).Once()
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
			c := newTestWorkspaceController(mockStorage)

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
					// Use Contains for checking wrapped errors, Is for specific errors.
					if errors.Is(tt.expectedError, storage.ErrResourceNotFound) {
						assert.ErrorIs(t, err, tt.expectedError)
					} else {
						assert.Contains(t, err.Error(), tt.expectedError.Error())
					}
				}
			} else {
				assert.NoError(t, err)
			}
			// Verify mock expectations.
			mockStorage.AssertExpectations(t)
		})
	}
}
