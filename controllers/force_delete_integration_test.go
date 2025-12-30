package controllers

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/supabase-community/gotrue-go/types"

	v1 "github.com/neutree-ai/neutree/api/v1"
	authmocks "github.com/neutree-ai/neutree/internal/auth/mocks"
	clustermocks "github.com/neutree-ai/neutree/internal/cluster/mocks"
	"github.com/neutree-ai/neutree/internal/gateway/mocks"
	orchestratormocks "github.com/neutree-ai/neutree/internal/orchestrator/mocks"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

// TestForceDelete_ModelRegistryController tests force delete for ModelRegistry
func TestForceDelete_ModelRegistryController(t *testing.T) {
	tests := []struct {
		name        string
		forceDelete bool
		wantPhase   v1.ModelRegistryPhase
		wantErr     bool
	}{
		{
			name:        "Normal delete success",
			forceDelete: false,
			wantPhase:   v1.ModelRegistryPhaseDELETED,
			wantErr:     false,
		},
		{
			name:        "Force delete success",
			forceDelete: true,
			wantPhase:   v1.ModelRegistryPhaseDELETED,
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}

			annotations := map[string]string{}
			if tt.forceDelete {
				annotations["neutree.ai/force-delete"] = forceDeleteAnnotationValue
			}

			obj := &v1.ModelRegistry{
				ID: 1,
				Metadata: &v1.Metadata{
					Name:              "test-registry",
					Workspace:         "test-ws",
					DeletionTimestamp: time.Now().Format(time.RFC3339Nano),
					Annotations:       annotations,
				},
				Spec: &v1.ModelRegistrySpec{
					Type: v1.BentoMLModelRegistryType,
					Url:  "file://localhost/tmp/test",
				},
				Status: &v1.ModelRegistryStatus{
					Phase: v1.ModelRegistryPhaseCONNECTED,
				},
			}

			// Mock updateStatus call
			mockStorage.On("UpdateModelRegistry", "1", mock.MatchedBy(func(r *v1.ModelRegistry) bool {
				return r.Status != nil && r.Status.Phase == tt.wantPhase
			})).Return(nil).Once()

			c := &ModelRegistryController{
				storage: mockStorage,
			}
			c.syncHandler = c.sync

			err := c.sync(obj)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			mockStorage.AssertExpectations(t)
		})
	}
}

// TestForceDelete_ClusterController tests force delete for Cluster
func TestForceDelete_ClusterController(t *testing.T) {
	tests := []struct {
		name        string
		forceDelete bool
		wantPhase   v1.ClusterPhase
		wantErr     bool
	}{
		{
			name:        "Normal delete success",
			forceDelete: false,
			wantPhase:   v1.ClusterPhaseDeleted,
			wantErr:     false,
		},
		{
			name:        "Force delete success",
			forceDelete: true,
			wantPhase:   v1.ClusterPhaseDeleted,
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			mockReconcile := &clustermocks.MockClusterReconcile{}
			mockGw := &mocks.MockGateway{}

			annotations := map[string]string{}
			if tt.forceDelete {
				annotations["neutree.ai/force-delete"] = forceDeleteAnnotationValue
			}

			obj := &v1.Cluster{
				ID: 1,
				Metadata: &v1.Metadata{
					Name:              "test-cluster",
					Workspace:         "test-ws",
					DeletionTimestamp: time.Now().Format(time.RFC3339Nano),
					Annotations:       annotations,
				},
				Spec: &v1.ClusterSpec{
					Type: v1.KubernetesClusterType,
				},
				Status: &v1.ClusterStatus{
					Phase:       v1.ClusterPhaseRunning,
					Initialized: true,
				},
			}

			// Mock gateway DeleteCluster
			mockGw.On("DeleteCluster", obj).Return(nil).Once()

			// Mock reconcile delete
			mockReconcile.On("ReconcileDelete", mock.Anything, obj).Return(nil).Once()

			// Mock UpdateCluster for status update
			mockStorage.On("UpdateCluster", "1", mock.MatchedBy(func(c *v1.Cluster) bool {
				return c.Status != nil && c.Status.Phase == tt.wantPhase
			})).Return(nil).Once()

			c := newTestClusterController(mockStorage, mockReconcile)
			c.gw = mockGw

			err := c.sync(obj)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			mockStorage.AssertExpectations(t)
			mockReconcile.AssertExpectations(t)
			mockGw.AssertExpectations(t)
		})
	}
}

// TestForceDelete_UserProfileController tests force delete for UserProfile
func TestForceDelete_UserProfileController(t *testing.T) {
	userID := uuid.New().String()
	userUUID, _ := uuid.Parse(userID)

	tests := []struct {
		name        string
		forceDelete bool
		deleteErr   error
		wantPhase   v1.UserProfilePhase
		wantErr     bool
	}{
		{
			name:        "Normal delete success",
			forceDelete: false,
			deleteErr:   nil,
			wantPhase:   v1.UserProfilePhaseDELETED,
			wantErr:     false,
		},
		{
			name:        "Normal delete fails -> FAILED phase and return error",
			forceDelete: false,
			deleteErr:   errors.New("gotrue api error"),
			wantPhase:   v1.UserProfilePhaseFAILED,
			wantErr:     true,
		},
		{
			name:        "Force delete success",
			forceDelete: true,
			deleteErr:   nil,
			wantPhase:   v1.UserProfilePhaseDELETED,
			wantErr:     false,
		},
		{
			name:        "Force delete with error -> DELETED phase, no error returned",
			forceDelete: true,
			deleteErr:   errors.New("gotrue api error"),
			wantPhase:   v1.UserProfilePhaseDELETED,
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := storagemocks.NewMockStorage(t)
			mockAuthClient := authmocks.NewMockClient(t)

			annotations := map[string]string{}
			if tt.forceDelete {
				annotations["neutree.ai/force-delete"] = forceDeleteAnnotationValue
			}

			obj := &v1.UserProfile{
				ID: userID,
				Metadata: &v1.Metadata{
					Name:              "test-user-" + userID,
					Workspace:         "test-workspace",
					DeletionTimestamp: time.Now().Format(time.RFC3339Nano),
					Annotations:       annotations,
				},
				Spec: &v1.UserProfileSpec{
					Email: "test@example.com",
				},
				Status: &v1.UserProfileStatus{
					Phase: v1.UserProfilePhaseCREATED,
				},
			}

			// Mock GoTrue deletion
			mockAuthClient.On("AdminDeleteUser", types.AdminDeleteUserRequest{
				UserID: userUUID,
			}).Return(tt.deleteErr).Once()

			// Mock status update
			mockStorage.On("UpdateUserProfile", userID, mock.MatchedBy(func(up *v1.UserProfile) bool {
				return up.Status != nil && up.Status.Phase == tt.wantPhase
			})).Return(nil).Once()

			controller := newTestUserProfileController(mockStorage, mockAuthClient)
			err := controller.sync(obj)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestForceDelete_ApiKeyController tests force delete for ApiKey
func TestForceDelete_ApiKeyController(t *testing.T) {
	tests := []struct {
		name        string
		forceDelete bool
		wantErr     bool
	}{
		{
			name:        "Normal delete success",
			forceDelete: false,
			wantErr:     false,
		},
		{
			name:        "Force delete success",
			forceDelete: true,
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			mockGw := &mocks.MockGateway{}

			annotations := map[string]string{}
			if tt.forceDelete {
				annotations["neutree.ai/force-delete"] = forceDeleteAnnotationValue
			}

			obj := &v1.ApiKey{
				ID: "test-id",
				Metadata: &v1.Metadata{
					Name:              "test-api-key",
					Workspace:         "test-ws",
					DeletionTimestamp: time.Now().Format(time.RFC3339Nano),
					Annotations:       annotations,
				},
				Status: &v1.ApiKeyStatus{
					Phase: v1.ApiKeyPhaseCREATED,
				},
			}

			// Mock gateway DeleteAPIKey
			mockGw.On("DeleteAPIKey", obj).Return(nil).Once()

			// Mock status update
			mockStorage.On("UpdateApiKey", "test-id", mock.MatchedBy(func(ak *v1.ApiKey) bool {
				return ak.Status != nil && ak.Status.Phase == v1.ApiKeyPhaseDELETED
			})).Return(nil).Once()

			c := newTestApiKeyController(mockStorage)
			c.gw = mockGw

			err := c.sync(obj)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			mockStorage.AssertExpectations(t)
			mockGw.AssertExpectations(t)
		})
	}
}

// TestForceDelete_ImageRegistryController tests deletion for ImageRegistry
// Note: ImageRegistry has no cleanup operations, so force delete behaves identically to normal delete
func TestForceDelete_ImageRegistryController(t *testing.T) {
	mockStorage := &storagemocks.MockStorage{}

	obj := &v1.ImageRegistry{
		ID: 1,
		Metadata: &v1.Metadata{
			Name:              "test-image-registry",
			Workspace:         "test-ws",
			DeletionTimestamp: time.Now().Format(time.RFC3339Nano),
		},
		Spec: &v1.ImageRegistrySpec{
			URL:        "https://registry.example.com",
			Repository: "test-repo",
		},
		Status: &v1.ImageRegistryStatus{
			Phase: v1.ImageRegistryPhaseCONNECTED,
		},
	}

	// Mock updateStatus call
	mockStorage.On("UpdateImageRegistry", "1", mock.MatchedBy(func(r *v1.ImageRegistry) bool {
		return r.Status != nil && r.Status.Phase == v1.ImageRegistryPhaseDELETED
	})).Return(nil).Once()

	c := &ImageRegistryController{
		storage: mockStorage,
	}
	c.syncHandler = c.sync

	err := c.sync(obj)

	assert.NoError(t, err)
	mockStorage.AssertExpectations(t)
}

// TestForceDelete_EndpointController tests force delete for Endpoint
func TestForceDelete_EndpointController(t *testing.T) {
	tests := []struct {
		name        string
		forceDelete bool
		wantPhase   v1.EndpointPhase
		wantErr     bool
	}{
		{
			name:        "Normal delete success",
			forceDelete: false,
			wantPhase:   v1.EndpointPhaseDELETED,
			wantErr:     false,
		},
		{
			name:        "Force delete success",
			forceDelete: true,
			wantPhase:   v1.EndpointPhaseDELETED,
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			mockGw := &mocks.MockGateway{}
			mockOrchestrator := &orchestratormocks.MockOrchestrator{}

			annotations := map[string]string{}
			if tt.forceDelete {
				annotations["neutree.ai/force-delete"] = forceDeleteAnnotationValue
			}

			obj := &v1.Endpoint{
				ID: 1,
				Metadata: &v1.Metadata{
					Name:              "test-endpoint",
					Workspace:         "test-ws",
					DeletionTimestamp: time.Now().Format(time.RFC3339Nano),
					Annotations:       annotations,
				},
				Spec: &v1.EndpointSpec{
					Cluster: "test-cluster",
					Model: &v1.ModelSpec{
						Name: "test-model",
					},
				},
				Status: &v1.EndpointStatus{
					Phase: v1.EndpointPhaseRUNNING,
				},
			}

			// Mock ListCluster for getOrchestrator (called multiple times during deletion)
			mockStorage.On("ListCluster", mock.Anything).Return([]v1.Cluster{
				{
					ID: 1,
					Metadata: &v1.Metadata{
						Name:      "test-cluster",
						Workspace: "test-ws",
					},
				},
			}, nil).Maybe()

			// Mock gateway DeleteEndpoint
			mockGw.On("DeleteEndpoint", obj).Return(nil).Once()

			// Mock orchestrator calls during endpoint deletion
			mockOrchestrator.On("DeleteEndpoint", obj).Return(nil).Maybe()
			mockOrchestrator.On("DisconnectEndpointModel", obj).Return(nil).Maybe()

			// Mock updateStatus call
			mockStorage.On("UpdateEndpoint", "1", mock.MatchedBy(func(e *v1.Endpoint) bool {
				return e.Status != nil && e.Status.Phase == tt.wantPhase
			})).Return(nil).Once()

			c := newTestEndpointController(mockStorage, mockOrchestrator)
			c.gw = mockGw

			err := c.sync(obj)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			mockStorage.AssertExpectations(t)
			mockGw.AssertExpectations(t)
			mockOrchestrator.AssertExpectations(t)
		})
	}
}

// TestForceDelete_WorkspaceController tests force delete for Workspace
func TestForceDelete_WorkspaceController(t *testing.T) {
	tests := []struct {
		name        string
		forceDelete bool
		wantPhase   v1.WorkspacePhase
		wantErr     bool
	}{
		{
			name:        "Normal delete success",
			forceDelete: false,
			wantPhase:   v1.WorkspacePhaseDELETED,
			wantErr:     false,
		},
		{
			name:        "Force delete success",
			forceDelete: true,
			wantPhase:   v1.WorkspacePhaseDELETED,
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}

			annotations := map[string]string{}
			if tt.forceDelete {
				annotations["neutree.ai/force-delete"] = forceDeleteAnnotationValue
			}

			obj := &v1.Workspace{
				ID: 1,
				Metadata: &v1.Metadata{
					Name:              "test-workspace",
					Workspace:         "test-workspace",
					DeletionTimestamp: time.Now().Format(time.RFC3339Nano),
					Annotations:       annotations,
				},
				Status: &v1.WorkspaceStatus{
					Phase: v1.WorkspacePhaseCREATED,
				},
			}

			// Mock ListEngine for DeleteWorkspaceEngine
			mockStorage.On("ListEngine", mock.Anything).Return([]v1.Engine{}, nil).Once()

			// Mock UpdateWorkspace for status update
			mockStorage.On("UpdateWorkspace", "1", mock.MatchedBy(func(w *v1.Workspace) bool {
				return w.Status != nil && w.Status.Phase == tt.wantPhase
			})).Return(nil).Once()

			c := &WorkspaceController{
				storage: mockStorage,
			}
			c.syncHandler = c.sync

			err := c.sync(obj)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			mockStorage.AssertExpectations(t)
		})
	}
}

// TestForceDelete_EngineController tests deletion for Engine
// Note: Engine has no cleanup operations, so force delete behaves identically to normal delete
func TestForceDelete_EngineController(t *testing.T) {
	mockStorage := &storagemocks.MockStorage{}

	obj := &v1.Engine{
		ID: 1,
		Metadata: &v1.Metadata{
			Name:              "test-engine",
			Workspace:         "test-ws",
			DeletionTimestamp: time.Now().Format(time.RFC3339Nano),
		},
		Status: &v1.EngineStatus{
			Phase: v1.EnginePhaseCreated,
		},
	}

	// Mock updateStatus call
	mockStorage.On("UpdateEngine", "1", mock.MatchedBy(func(e *v1.Engine) bool {
		return e.Status != nil && e.Status.Phase == v1.EnginePhaseDeleted
	})).Return(nil).Once()

	c := &EngineController{
		storage: mockStorage,
	}
	c.syncHandler = c.sync

	err := c.sync(obj)

	assert.NoError(t, err)
	mockStorage.AssertExpectations(t)
}

// TestForceDelete_ModelCatalogController tests deletion for ModelCatalog
// Note: ModelCatalog has no cleanup operations, so force delete behaves identically to normal delete
func TestForceDelete_ModelCatalogController(t *testing.T) {
	mockStorage := &storagemocks.MockStorage{}

	obj := &v1.ModelCatalog{
		ID: 1,
		Metadata: &v1.Metadata{
			Name:              "test-model-catalog",
			Workspace:         "test-ws",
			DeletionTimestamp: time.Now().Format(time.RFC3339Nano),
		},
		Status: &v1.ModelCatalogStatus{
			Phase: v1.ModelCatalogPhaseREADY,
		},
	}

	// Mock updateStatus call
	mockStorage.On("UpdateModelCatalog", "1", mock.MatchedBy(func(mc *v1.ModelCatalog) bool {
		return mc.Status != nil && mc.Status.Phase == v1.ModelCatalogPhaseDELETED
	})).Return(nil).Once()

	c := &ModelCatalogController{
		storage: mockStorage,
	}
	c.syncHandler = c.sync

	err := c.sync(obj)

	assert.NoError(t, err)
	mockStorage.AssertExpectations(t)
}

// TestForceDelete_RoleController tests deletion for Role
// Note: Role has no cleanup operations, so force delete behaves identically to normal delete
func TestForceDelete_RoleController(t *testing.T) {
	mockStorage := &storagemocks.MockStorage{}

	obj := &v1.Role{
		ID: 1,
		Metadata: &v1.Metadata{
			Name:              "test-role",
			Workspace:         "test-ws",
			DeletionTimestamp: time.Now().Format(time.RFC3339Nano),
		},
		Status: &v1.RoleStatus{
			Phase: v1.RolePhaseCREATED,
		},
	}

	// Mock updateStatus call
	mockStorage.On("UpdateRole", "1", mock.MatchedBy(func(r *v1.Role) bool {
		return r.Status != nil && r.Status.Phase == v1.RolePhaseDELETED
	})).Return(nil).Once()

	c := &RoleController{
		storage: mockStorage,
	}
	c.syncHandler = c.sync

	err := c.sync(obj)

	assert.NoError(t, err)
	mockStorage.AssertExpectations(t)
}

// TestForceDelete_RoleAssignmentController tests deletion for RoleAssignment
// Note: RoleAssignment has no cleanup operations, so force delete behaves identically to normal delete
func TestForceDelete_RoleAssignmentController(t *testing.T) {
	mockStorage := &storagemocks.MockStorage{}

	obj := &v1.RoleAssignment{
		ID: 1,
		Metadata: &v1.Metadata{
			Name:              "test-role-assignment",
			Workspace:         "test-ws",
			DeletionTimestamp: time.Now().Format(time.RFC3339Nano),
		},
		Status: &v1.RoleAssignmentStatus{
			Phase: v1.RoleAssignmentPhaseCREATED,
		},
	}

	// Mock updateStatus call
	mockStorage.On("UpdateRoleAssignment", "1", mock.MatchedBy(func(ra *v1.RoleAssignment) bool {
		return ra.Status != nil && ra.Status.Phase == v1.RoleAssignmentPhaseDELETED
	})).Return(nil).Once()

	c := &RoleAssignmentController{
		storage: mockStorage,
	}
	c.syncHandler = c.sync

	err := c.sync(obj)

	assert.NoError(t, err)
	mockStorage.AssertExpectations(t)
}
