package proxies

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/pkg/storage"
	storageMocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func TestValidateUserProfileDeletion(t *testing.T) {
	tests := []struct {
		name                string
		workspace           string
		username            string
		userID              string
		userProfiles        []v1.UserProfile
		listError           error
		roleAssignmentCount int
		countError          error
		expectError         bool
		expectedCode        string
		expectedHint        string
	}{
		{
			name:      "no dependencies - deletion allowed",
			workspace: "default",
			username:  "testuser",
			userID:    "user-uuid-123",
			userProfiles: []v1.UserProfile{
				{ID: "user-uuid-123"},
			},
			roleAssignmentCount: 0,
			expectError:         false,
		},
		{
			name:      "has dependencies - deletion blocked",
			workspace: "default",
			username:  "testuser",
			userID:    "user-uuid-123",
			userProfiles: []v1.UserProfile{
				{ID: "user-uuid-123"},
			},
			roleAssignmentCount: 2,
			expectError:         true,
			expectedCode:        "10130",
			expectedHint:        "2 role assignment(s) still reference this user",
		},
		{
			name:         "user not found - deletion allowed",
			workspace:    "default",
			username:     "nonexistent",
			userProfiles: []v1.UserProfile{},
			expectError:  false,
		},
		{
			name:        "list user profile error",
			workspace:   "default",
			username:    "testuser",
			listError:   errors.New("database error"),
			expectError: true,
		},
		{
			name:      "count role assignments error",
			workspace: "default",
			username:  "testuser",
			userID:    "user-uuid-123",
			userProfiles: []v1.UserProfile{
				{ID: "user-uuid-123"},
			},
			countError:  errors.New("database error"),
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := storageMocks.NewMockStorage(t)

			mockStorage.On("ListUserProfile", storage.ListOption{
				Filters: []storage.Filter{{Column: "metadata->>name", Operator: "eq", Value: tt.username}},
			}).Return(tt.userProfiles, tt.listError)

			if tt.listError == nil && len(tt.userProfiles) > 0 {
				mockStorage.On("Count",
					storage.ROLE_ASSIGNMENT_TABLE,
					[]storage.Filter{
						{Column: "spec->>user_id", Operator: "eq", Value: tt.userID},
					},
				).Return(tt.roleAssignmentCount, tt.countError)
			}

			validator := validateUserProfileDeletion(mockStorage)
			err := validator(tt.workspace, tt.username)

			if tt.expectError {
				assert.Error(t, err)

				if tt.listError == nil && tt.countError == nil {
					deletionErr, ok := err.(*middleware.DeletionError)
					assert.True(t, ok, "error should be DeletionError")
					if ok {
						assert.Equal(t, tt.expectedCode, deletionErr.Code)
						assert.Contains(t, deletionErr.Hint, tt.expectedHint)
					}
				}
			} else {
				assert.NoError(t, err)
			}

			mockStorage.AssertExpectations(t)
		})
	}
}
