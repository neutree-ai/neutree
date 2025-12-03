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
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func newTestUserProfileController(storage *storagemocks.MockStorage, authClient *authmocks.MockClient) *UserProfileController {
	c, _ := NewUserProfileController(&UserProfileControllerOption{
		Storage:    storage,
		AuthClient: authClient,
	})
	return c
}

func testUserProfile(id string, phase v1.UserProfilePhase) *v1.UserProfile {
	profile := &v1.UserProfile{
		ID: id,
		Metadata: &v1.Metadata{
			Name:      "test-user-" + id,
			Workspace: "test-workspace",
		},
		Spec: &v1.UserProfileSpec{
			Email: "test@example.com",
		},
	}
	if phase != "" {
		profile.Status = &v1.UserProfileStatus{Phase: phase}
	}
	return profile
}

func testUserProfileWithDeletion(id string, phase v1.UserProfilePhase) *v1.UserProfile {
	profile := testUserProfile(id, phase)
	profile.Metadata.DeletionTimestamp = time.Now().Format(time.RFC3339Nano)
	return profile
}

func TestUserProfileController_Sync_Creation(t *testing.T) {
	userID := uuid.New().String()

	tests := []struct {
		name      string
		input     *v1.UserProfile
		mockSetup func(*storagemocks.MockStorage, *authmocks.MockClient)
		wantErr   bool
	}{
		{
			name:  "PENDING -> CREATED (success)",
			input: testUserProfile(userID, v1.UserProfilePhasePENDING),
			mockSetup: func(s *storagemocks.MockStorage, a *authmocks.MockClient) {
				a.On("AdminUpdateUser", mock.MatchedBy(func(req types.AdminUpdateUserRequest) bool {
					return req.Email == "test@example.com" && req.EmailConfirm == true
				})).Return(&types.AdminUpdateUserResponse{}, nil).Once()

				s.On("UpdateUserProfile", userID, mock.MatchedBy(func(up *v1.UserProfile) bool {
					return up.Status != nil &&
						up.Status.Phase == v1.UserProfilePhaseCREATED &&
						up.Status.ErrorMessage == "" &&
						up.Status.SyncedSpec != nil &&
						up.Status.SyncedSpec.Email == "test@example.com"
				})).Return(nil).Once()
			},
			wantErr: false,
		},
		{
			name:  "No status -> CREATED (success)",
			input: testUserProfile(userID, ""),
			mockSetup: func(s *storagemocks.MockStorage, a *authmocks.MockClient) {
				a.On("AdminUpdateUser", mock.Anything).Return(&types.AdminUpdateUserResponse{}, nil).Once()

				s.On("UpdateUserProfile", userID, mock.MatchedBy(func(up *v1.UserProfile) bool {
					return up.Status != nil &&
						up.Status.Phase == v1.UserProfilePhaseCREATED &&
						up.Status.SyncedSpec != nil
				})).Return(nil).Once()
			},
			wantErr: false,
		},
		{
			name:  "PENDING -> CREATED (GoTrue sync failed)",
			input: testUserProfile(userID, v1.UserProfilePhasePENDING),
			mockSetup: func(s *storagemocks.MockStorage, a *authmocks.MockClient) {
				a.On("AdminUpdateUser", mock.Anything).Return(nil, assert.AnError).Once()

				s.On("UpdateUserProfile", userID, mock.MatchedBy(func(up *v1.UserProfile) bool {
					return up.Status != nil && up.Status.Phase == v1.UserProfilePhaseFAILED
				})).Return(nil).Once()
			},
			wantErr: true,
		},
		{
			name:  "CREATED -> no change (no sync needed)",
			input: func() *v1.UserProfile {
				up := testUserProfile(userID, v1.UserProfilePhaseCREATED)
				up.Status.SyncedSpec = &v1.UserProfileSpec{
					Email: "test@example.com",
				}
				return up
			}(),
			mockSetup: func(_ *storagemocks.MockStorage, _ *authmocks.MockClient) {
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := storagemocks.NewMockStorage(t)
			mockAuthClient := authmocks.NewMockClient(t)
			tt.mockSetup(mockStorage, mockAuthClient)

			controller := newTestUserProfileController(mockStorage, mockAuthClient)
			err := controller.sync(tt.input)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestUserProfileController_Sync_Deletion_Phase2(t *testing.T) {
	userID := uuid.New().String()

	tests := []struct {
		name      string
		input     *v1.UserProfile
		mockSetup func(*storagemocks.MockStorage, *authmocks.MockClient)
		wantErr   bool
	}{
		{
			name:  "Phase=DELETED -> Delete from DB (success)",
			input: testUserProfileWithDeletion(userID, v1.UserProfilePhaseDELETED),
			mockSetup: func(s *storagemocks.MockStorage, _ *authmocks.MockClient) {
				s.On("DeleteUserProfile", userID).Return(nil).Once()
			},
			wantErr: false,
		},
		{
			name:  "Phase=DELETED -> Delete from DB (failed)",
			input: testUserProfileWithDeletion(userID, v1.UserProfilePhaseDELETED),
			mockSetup: func(s *storagemocks.MockStorage, _ *authmocks.MockClient) {
				s.On("DeleteUserProfile", userID).Return(assert.AnError).Once()
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := storagemocks.NewMockStorage(t)
			mockAuthClient := authmocks.NewMockClient(t)
			tt.mockSetup(mockStorage, mockAuthClient)

			controller := newTestUserProfileController(mockStorage, mockAuthClient)
			err := controller.sync(tt.input)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestUserProfileController_Sync_Deletion_Phase1(t *testing.T) {
	userID := uuid.New().String()
	userUUID, _ := uuid.Parse(userID)

	tests := []struct {
		name      string
		input     *v1.UserProfile
		mockSetup func(*storagemocks.MockStorage, *authmocks.MockClient)
		wantErr   bool
		wantPhase v1.UserProfilePhase
	}{
		{
			name:  "User exists -> Delete from GoTrue -> Mark DELETED",
			input: testUserProfileWithDeletion(userID, v1.UserProfilePhaseCREATED),
			mockSetup: func(s *storagemocks.MockStorage, a *authmocks.MockClient) {
				// Delete from GoTrue succeeds
				a.On("AdminDeleteUser", types.AdminDeleteUserRequest{
					UserID: userUUID,
				}).Return(nil).Once()

				// Update status to DELETED
				s.On("UpdateUserProfile", userID, mock.MatchedBy(func(up *v1.UserProfile) bool {
					return up.Status != nil &&
						up.Status.Phase == v1.UserProfilePhaseDELETED &&
						up.Status.ErrorMessage == ""
				})).Return(nil).Once()
			},
			wantErr:   false,
			wantPhase: v1.UserProfilePhaseDELETED,
		},
		{
			name:  "User doesn't exist in GoTrue -> Skip deletion -> Mark DELETED",
			input: testUserProfileWithDeletion(userID, v1.UserProfilePhaseCREATED),
			mockSetup: func(s *storagemocks.MockStorage, a *authmocks.MockClient) {
				// Delete returns "not found" error
				a.On("AdminDeleteUser", types.AdminDeleteUserRequest{
					UserID: userUUID,
				}).Return(errors.New("User not found")).Once()

				// Update status to DELETED (user already gone)
				s.On("UpdateUserProfile", userID, mock.MatchedBy(func(up *v1.UserProfile) bool {
					return up.Status != nil && up.Status.Phase == v1.UserProfilePhaseDELETED
				})).Return(nil).Once()
			},
			wantErr:   false,
			wantPhase: v1.UserProfilePhaseDELETED,
		},
		{
			name:  "GoTrue deletion fails -> Mark FAILED",
			input: testUserProfileWithDeletion(userID, v1.UserProfilePhaseCREATED),
			mockSetup: func(s *storagemocks.MockStorage, a *authmocks.MockClient) {
				// Delete from GoTrue fails (error does not contain "xyz")
				deleteError := errors.New("gotrue api error")
				a.On("AdminDeleteUser", types.AdminDeleteUserRequest{
					UserID: userUUID,
				}).Return(deleteError).Once()

				// Update status to FAILED with error message
				s.On("UpdateUserProfile", userID, mock.MatchedBy(func(up *v1.UserProfile) bool {
					return up.Status != nil &&
						up.Status.Phase == v1.UserProfilePhaseFAILED &&
						up.Status.ErrorMessage != ""
				})).Return(nil).Once()
			},
			wantErr:   true,
			wantPhase: v1.UserProfilePhaseFAILED,
		},
		{
			name:  "Status update fails after successful GoTrue deletion",
			input: testUserProfileWithDeletion(userID, v1.UserProfilePhaseCREATED),
			mockSetup: func(s *storagemocks.MockStorage, a *authmocks.MockClient) {
				// GoTrue deletion succeeds
				a.On("AdminDeleteUser", types.AdminDeleteUserRequest{
					UserID: userUUID,
				}).Return(nil).Once()

				// But status update fails
				s.On("UpdateUserProfile", userID, mock.Anything).Return(assert.AnError).Once()
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := storagemocks.NewMockStorage(t)
			mockAuthClient := authmocks.NewMockClient(t)
			tt.mockSetup(mockStorage, mockAuthClient)

			controller := newTestUserProfileController(mockStorage, mockAuthClient)
			err := controller.sync(tt.input)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestUserProfileController_DeleteGoTrueUser(t *testing.T) {
	userID := uuid.New().String()
	userUUID, _ := uuid.Parse(userID)

	tests := []struct {
		name      string
		userID    string
		mockSetup func(*authmocks.MockClient)
		wantErr   bool
	}{
		{
			name:   "Valid UUID, user exists, deletion succeeds",
			userID: userID,
			mockSetup: func(a *authmocks.MockClient) {
				a.On("AdminDeleteUser", types.AdminDeleteUserRequest{
					UserID: userUUID,
				}).Return(nil).Once()
			},
			wantErr: false,
		},
		{
			name:   "User doesn't exist in GoTrue",
			userID: userID,
			mockSetup: func(a *authmocks.MockClient) {
				a.On("AdminDeleteUser", types.AdminDeleteUserRequest{
					UserID: userUUID,
				}).Return(errors.New("User not found")).Once()
			},
			wantErr: false, // Treated as success
		},
		{
			name:   "User exists, deletion fails",
			userID: userID,
			mockSetup: func(a *authmocks.MockClient) {
				a.On("AdminDeleteUser", types.AdminDeleteUserRequest{
					UserID: userUUID,
				}).Return(errors.New("gotrue api error")).Once()
			},
			wantErr: true,
		},
		{
			name:   "Invalid UUID format",
			userID: "not-a-uuid",
			mockSetup: func(_ *authmocks.MockClient) {
				// No mock expectations
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := storagemocks.NewMockStorage(t)
			mockAuthClient := authmocks.NewMockClient(t)
			tt.mockSetup(mockAuthClient)

			controller := newTestUserProfileController(mockStorage, mockAuthClient)

			profile := testUserProfile(tt.userID, "")
			err := controller.deleteGoTrueUser(profile)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestUserProfileController_Sync_EmailUpdate(t *testing.T) {
	userID := uuid.New().String()

	tests := []struct {
		name      string
		input     *v1.UserProfile
		mockSetup func(*storagemocks.MockStorage, *authmocks.MockClient)
		wantErr   bool
	}{
		{
			name: "Email changed -> Sync to GoTrue (success)",
			input: func() *v1.UserProfile {
				up := testUserProfile(userID, v1.UserProfilePhaseCREATED)
				up.Spec.Email = "newemail@example.com"
				up.Status.SyncedSpec = &v1.UserProfileSpec{
					Email: "oldemail@example.com",
				}
				return up
			}(),
			mockSetup: func(s *storagemocks.MockStorage, a *authmocks.MockClient) {
				a.On("AdminUpdateUser", mock.MatchedBy(func(req types.AdminUpdateUserRequest) bool {
					return req.Email == "newemail@example.com" && req.EmailConfirm == true
				})).Return(&types.AdminUpdateUserResponse{}, nil).Once()

				s.On("UpdateUserProfile", userID, mock.MatchedBy(func(up *v1.UserProfile) bool {
					return up.Status != nil &&
						up.Status.Phase == v1.UserProfilePhaseCREATED &&
						up.Status.SyncedSpec != nil &&
						up.Status.SyncedSpec.Email == "newemail@example.com"
				})).Return(nil).Once()
			},
			wantErr: false,
		},
		{
			name: "Email changed -> Sync failed",
			input: func() *v1.UserProfile {
				up := testUserProfile(userID, v1.UserProfilePhaseCREATED)
				up.Spec.Email = "newemail@example.com"
				up.Status.SyncedSpec = &v1.UserProfileSpec{
					Email: "oldemail@example.com",
				}
				return up
			}(),
			mockSetup: func(s *storagemocks.MockStorage, a *authmocks.MockClient) {
				a.On("AdminUpdateUser", mock.Anything).Return(nil, assert.AnError).Once()

				s.On("UpdateUserProfile", userID, mock.MatchedBy(func(up *v1.UserProfile) bool {
					return up.Status != nil &&
						up.Status.Phase == v1.UserProfilePhaseFAILED &&
						up.Status.SyncedSpec != nil &&
						up.Status.SyncedSpec.Email == "oldemail@example.com"
				})).Return(nil).Once()
			},
			wantErr: true,
		},
		{
			name: "No synced spec -> Sync to GoTrue",
			input: func() *v1.UserProfile {
				up := testUserProfile(userID, v1.UserProfilePhaseCREATED)
				up.Status.SyncedSpec = nil
				return up
			}(),
			mockSetup: func(s *storagemocks.MockStorage, a *authmocks.MockClient) {
				a.On("AdminUpdateUser", mock.Anything).Return(&types.AdminUpdateUserResponse{}, nil).Once()

				s.On("UpdateUserProfile", userID, mock.MatchedBy(func(up *v1.UserProfile) bool {
					return up.Status != nil &&
						up.Status.Phase == v1.UserProfilePhaseCREATED &&
						up.Status.SyncedSpec != nil
				})).Return(nil).Once()
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := storagemocks.NewMockStorage(t)
			mockAuthClient := authmocks.NewMockClient(t)
			tt.mockSetup(mockStorage, mockAuthClient)

			controller := newTestUserProfileController(mockStorage, mockAuthClient)
			err := controller.sync(tt.input)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
