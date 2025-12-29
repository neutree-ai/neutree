package proxies

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/pkg/storage"
	storageMocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func TestValidateUserProfileDeletion(t *testing.T) {
	tests := []struct {
		name                string
		workspace           string
		userID              string
		roleAssignmentCount int
		queryError          error
		expectError         bool
		expectedCode        string
		expectedHint        string
	}{
		{
			name:                "no dependencies - deletion allowed",
			workspace:           "default",
			userID:              "user-123",
			roleAssignmentCount: 0,
			queryError:          nil,
			expectError:         false,
		},
		{
			name:                "has dependencies - deletion blocked",
			workspace:           "default",
			userID:              "user-123",
			roleAssignmentCount: 2,
			queryError:          nil,
			expectError:         true,
			expectedCode:        "10130",
			expectedHint:        "2 role assignment(s) still reference this user",
		},
		{
			name:                "query error",
			workspace:           "default",
			userID:              "user-123",
			roleAssignmentCount: 0,
			queryError:          errors.New("database error"),
			expectError:         true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := storageMocks.NewMockStorage(t)

			mockStorage.On("Count",
				storage.ROLE_ASSIGNMENT_TABLE,
				[]storage.Filter{
					{Column: "spec->>user_id", Operator: "eq", Value: tt.userID},
				},
			).Return(tt.roleAssignmentCount, tt.queryError)

			validator := validateUserProfileDeletion(mockStorage)
			err := validator(tt.workspace, tt.userID)

			if tt.expectError {
				assert.Error(t, err)

				if tt.queryError == nil {
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
