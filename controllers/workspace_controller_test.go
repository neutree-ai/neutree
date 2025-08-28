package controllers

import (
	"strconv"
	"testing"
	"time"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	v1 "github.com/neutree-ai/neutree/api/v1"
	acceleratormocks "github.com/neutree-ai/neutree/internal/accelerator/mocks"
	"github.com/neutree-ai/neutree/pkg/storage"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

// newTestWorkspaceController is a helper to create a WorkspaceController with mocked storage for testing.
func newTestWorkspaceController(storage *storagemocks.MockStorage, acceleratorManager *acceleratormocks.MockManager) *WorkspaceController {
	c, _ := NewWorkspaceController(&WorkspaceControllerOption{
		Storage:            storage,
		AcceleratorManager: acceleratorManager,
	})

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
		mockSetup func(*storagemocks.MockStorage, *acceleratormocks.MockManager)
		wantErr   bool
	}{
		{
			name:  "Deleting (Phase=DELETED) -> Deleted (DB delete success)",
			input: testWorkspaceWithDeletionTimestamp(workspaceID, v1.WorkspacePhaseDELETED),
			mockSetup: func(s *storagemocks.MockStorage, a *acceleratormocks.MockManager) {
				s.On("DeleteWorkspace", workspaceIDStr).Return(nil).Once()
			},
			wantErr: false,
		},
		{
			name:  "Deleting (Phase=DELETED) -> Error (DB delete failed)",
			input: testWorkspaceWithDeletionTimestamp(workspaceID, v1.WorkspacePhaseDELETED),
			mockSetup: func(s *storagemocks.MockStorage, a *acceleratormocks.MockManager) {
				s.On("DeleteWorkspace", workspaceIDStr).Return(assert.AnError).Once()
			},
			wantErr: true,
		},
		{
			name:  "Deleting (Phase=DELETED) -> Already Deleted (DB delete returns NotFound)",
			input: testWorkspaceWithDeletionTimestamp(workspaceID, v1.WorkspacePhaseDELETED),
			mockSetup: func(s *storagemocks.MockStorage, a *acceleratormocks.MockManager) {
				s.On("DeleteWorkspace", workspaceIDStr).Return(storage.ErrResourceNotFound).Once()
			},
			wantErr: false, // ErrResourceNotFound during delete is not an error for the controller
		},
		{
			name:  "Deleting (Phase=CREATED) -> Set Phase=DELETED (Update success)",
			input: testWorkspaceWithDeletionTimestamp(workspaceID, v1.WorkspacePhaseCREATED),
			mockSetup: func(s *storagemocks.MockStorage, a *acceleratormocks.MockManager) {
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
			mockSetup: func(s *storagemocks.MockStorage, a *acceleratormocks.MockManager) {
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
			mockSetup: func(s *storagemocks.MockStorage, a *acceleratormocks.MockManager) {
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
			mockAcceleratorManager := &acceleratormocks.MockManager{}
			tt.mockSetup(mockStorage, mockAcceleratorManager)
			c := newTestWorkspaceController(mockStorage, mockAcceleratorManager)

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
		mockSetup func(*storagemocks.MockStorage, *acceleratormocks.MockManager)
		wantErr   bool
	}{
		{
			name:  "No Status -> Set Phase=CREATED (Update success)",
			input: testWorkspace(workspaceID, ""),
			mockSetup: func(s *storagemocks.MockStorage, a *acceleratormocks.MockManager) {
				s.On("UpdateWorkspace", workspaceIDStr, mock.MatchedBy(func(r *v1.Workspace) bool {
					return r.Status != nil && r.Status.Phase == v1.WorkspacePhaseCREATED && r.Status.ErrorMessage == ""
				})).Return(nil).Once()
			},
			wantErr: false,
		},
		{
			name:  "Phase=PENDING -> Set Phase=CREATED (Update success)",
			input: testWorkspace(workspaceID, v1.WorkspacePhasePENDING),
			mockSetup: func(s *storagemocks.MockStorage, a *acceleratormocks.MockManager) {
				s.On("UpdateWorkspace", workspaceIDStr, mock.MatchedBy(func(r *v1.Workspace) bool {
					return r.Status != nil && r.Status.Phase == v1.WorkspacePhaseCREATED && r.Status.ErrorMessage == ""
				})).Return(nil).Once()
			},
			wantErr: false,
		},
		{
			name:  "Phase=PENDING -> Set Phase=CREATED (Update failed)",
			input: testWorkspace(workspaceID, v1.WorkspacePhasePENDING),
			mockSetup: func(s *storagemocks.MockStorage, a *acceleratormocks.MockManager) {
				s.On("UpdateWorkspace", workspaceIDStr, mock.MatchedBy(func(r *v1.Workspace) bool {
					return r.Status != nil && r.Status.Phase == v1.WorkspacePhaseCREATED
				})).Return(assert.AnError).Once()
			},
			wantErr: true,
		},
		{
			name:  "Phase=CREATED -> No Change",
			input: testWorkspace(workspaceID, v1.WorkspacePhaseCREATED),
			mockSetup: func(s *storagemocks.MockStorage, a *acceleratormocks.MockManager) {
				a.On("GetAllAcceleratorSupportEngines", mock.Anything).Return([]*v1.Engine{
					{
						Metadata: &v1.Metadata{
							Name: "test-engine",
						},
					},
				}, nil)
				s.On("ListEngine", mock.Anything).Return(nil, nil)
				s.On("CreateEngine", mock.Anything).Return(nil)
			},
			wantErr: false,
		},
		{
			name:  "Phase=DELETED (no deletionTimestamp) -> No Change",
			input: testWorkspace(workspaceID, v1.WorkspacePhaseDELETED),
			mockSetup: func(s *storagemocks.MockStorage, a *acceleratormocks.MockManager) {
				// Expect no calls to UpdateWorkspace or DeleteWorkspace.
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			mockAcceleratorManager := &acceleratormocks.MockManager{}
			tt.mockSetup(mockStorage, mockAcceleratorManager)
			c := newTestWorkspaceController(mockStorage, mockAcceleratorManager)

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

func TestWorkspaceController_Reconcile(t *testing.T) {
	workspaceID := 1
	failedWorkspaceID := 2
	workspaceIDStr := strconv.Itoa(workspaceID)
	// mockSyncHandler provides a controllable sync function for Reconcile tests.
	mockSyncHandler := func(obj *v1.Workspace) error {
		// Check for a condition to simulate failure.
		if obj != nil && obj.ID == failedWorkspaceID {
			return errors.New("mock sync failed")
		}
		// Simulate successful sync.
		return nil
	}

	tests := []struct {
		name          string
		inputKey      interface{}
		mockSetup     func(*storagemocks.MockStorage, *acceleratormocks.MockManager)
		useMockSync   bool  // Flag to indicate if the mock syncHandler should be used.
		expectedError error // Expected contained error string for specific checks.
		wantErr       bool
	}{
		{
			name:     "Reconcile success (real sync, no status change)", // Test scenario using default sync handler.
			inputKey: testWorkspace(workspaceID, v1.WorkspacePhaseCREATED),
			mockSetup: func(s *storagemocks.MockStorage, a *acceleratormocks.MockManager) {
				a.On("GetAllAcceleratorSupportEngines", mock.Anything).Return([]*v1.Engine{
					{
						Metadata: &v1.Metadata{
							Name: "test-engine",
						},
					},
				}, nil)
				s.On("ListEngine", mock.Anything).Return(nil, nil)
				s.On("CreateEngine", mock.Anything).Return(nil)
			},
			useMockSync: false, // Use the default c.sync via syncHandler.
			wantErr:     false,
		},
		{
			name:     "Reconcile success (real sync, status updated)", // Test scenario using default sync handler.
			inputKey: testWorkspace(workspaceID, v1.WorkspacePhasePENDING),
			mockSetup: func(s *storagemocks.MockStorage, a *acceleratormocks.MockManager) {
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
			inputKey: testWorkspace(workspaceID, v1.WorkspacePhaseCREATED),
			mockSetup: func(s *storagemocks.MockStorage, a *acceleratormocks.MockManager) {
			},
			useMockSync: true, // Override with mockSyncHandler.
			wantErr:     false,
		},
		{
			name:     "Invalid key type",
			inputKey: "not-an-obj",
			mockSetup: func(s *storagemocks.MockStorage, a *acceleratormocks.MockManager) {
				// No storage calls expected.
			},
			useMockSync:   false, // Fails before sync handler.
			wantErr:       true,
			expectedError: errors.New("failed to assert obj to *v1.Workspace"),
		},
		{
			name:     "Sync handler returns error (mock sync)",
			inputKey: testWorkspace(failedWorkspaceID, v1.WorkspacePhaseCREATED),
			mockSetup: func(s *storagemocks.MockStorage, a *acceleratormocks.MockManager) {
			},
			useMockSync: true, // Use the mock handler.
			wantErr:     true, // Expect error from mock sync handler to be propagated.
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			mockAcceleratorManager := &acceleratormocks.MockManager{}
			if tt.mockSetup != nil {
				tt.mockSetup(mockStorage, mockAcceleratorManager)
			}

			// Create controller using the helper.
			c := newTestWorkspaceController(mockStorage, mockAcceleratorManager)

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
