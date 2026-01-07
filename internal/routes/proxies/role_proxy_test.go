package proxies

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/pkg/storage"
	storageMocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func TestValidateRoleDeletion(t *testing.T) {
	tests := []struct {
		name                string
		workspace           string
		roleName            string
		roleAssignmentCount int
		queryError          error
		expectError         bool
		expectedCode        string
		expectedHint        string
		expectedMessagePart string
	}{
		{
			name:                "no dependencies - deletion allowed for workspace role",
			workspace:           "default",
			roleName:            "developer",
			roleAssignmentCount: 0,
			queryError:          nil,
			expectError:         false,
		},
		{
			name:                "no dependencies - deletion allowed for global role",
			workspace:           "",
			roleName:            "admin",
			roleAssignmentCount: 0,
			queryError:          nil,
			expectError:         false,
		},
		{
			name:                "has dependencies - deletion blocked for workspace role",
			workspace:           "default",
			roleName:            "developer",
			roleAssignmentCount: 3,
			queryError:          nil,
			expectError:         true,
			expectedCode:        "10129",
			expectedHint:        "3 role assignment(s) still reference this role",
			expectedMessagePart: "default/developer",
		},
		{
			name:                "has dependencies - deletion blocked for global role",
			workspace:           "",
			roleName:            "admin",
			roleAssignmentCount: 5,
			queryError:          nil,
			expectError:         true,
			expectedCode:        "10129",
			expectedHint:        "5 role assignment(s) still reference this role",
			expectedMessagePart: "global/admin",
		},
		{
			name:                "query error",
			workspace:           "default",
			roleName:            "developer",
			roleAssignmentCount: 0,
			queryError:          errors.New("database error"),
			expectError:         true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := storageMocks.NewMockStorage(t)

			expectedFilters := []storage.Filter{
				{Column: "spec->>role", Operator: "eq", Value: tt.roleName},
			}
			if tt.workspace == "" {
				expectedFilters = append(expectedFilters, storage.Filter{
					Column:   "metadata->>workspace",
					Operator: "is",
					Value:    "null",
				})
			} else {
				expectedFilters = append(expectedFilters, storage.Filter{
					Column:   "metadata->>workspace",
					Operator: "eq",
					Value:    tt.workspace,
				})
			}

			mockStorage.On("Count",
				storage.ROLE_ASSIGNMENT_TABLE,
				expectedFilters,
			).Return(tt.roleAssignmentCount, tt.queryError)

			validator := validateRoleDeletion(mockStorage)
			err := validator(tt.workspace, tt.roleName)

			if tt.expectError {
				assert.Error(t, err)

				if tt.queryError == nil {
					deletionErr, ok := err.(*middleware.DeletionError)
					assert.True(t, ok, "error should be DeletionError")
					if ok {
						assert.Equal(t, tt.expectedCode, deletionErr.Code)
						assert.Contains(t, deletionErr.Hint, tt.expectedHint)
						assert.Contains(t, deletionErr.Message, tt.expectedMessagePart)
					}
				}
			} else {
				assert.NoError(t, err)
			}

			mockStorage.AssertExpectations(t)
		})
	}
}
