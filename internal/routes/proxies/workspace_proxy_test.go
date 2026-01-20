package proxies

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/pkg/storage"
	storageMocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func TestValidateWorkspaceDeletion(t *testing.T) {
	tests := []struct {
		name          string
		workspace     string
		workspaceName string
		counts        map[string]int
		queryError    error
		expectError   bool
		expectedCode  string
		expectedHints []string
	}{
		{
			name:          "no dependencies - deletion allowed",
			workspace:     "",
			workspaceName: "default",
			counts: map[string]int{
				storage.ENDPOINT_TABLE:        0,
				storage.CLUSTERS_TABLE:        0,
				storage.MODEL_REGISTRY_TABLE:  0,
				storage.IMAGE_REGISTRY_TABLE:  0,
				storage.MODEL_CATALOG_TABLE:   0,
				storage.ROLE_TABLE:            0,
				storage.API_KEY_TABLE:         0,
				storage.ROLE_ASSIGNMENT_TABLE: 0,
			},
			queryError:  nil,
			expectError: false,
		},
		{
			name:          "has dependencies - deletion blocked",
			workspace:     "",
			workspaceName: "default",
			counts: map[string]int{
				storage.ENDPOINT_TABLE:        2,
				storage.CLUSTERS_TABLE:        1,
				storage.MODEL_REGISTRY_TABLE:  0,
				storage.IMAGE_REGISTRY_TABLE:  0,
				storage.MODEL_CATALOG_TABLE:   0,
				storage.ROLE_TABLE:            3,
				storage.API_KEY_TABLE:         0,
				storage.ROLE_ASSIGNMENT_TABLE: 0,
			},
			queryError:   nil,
			expectError:  true,
			expectedCode: "10125",
			expectedHints: []string{
				storage.ENDPOINT_TABLE + ": 2",
				storage.CLUSTERS_TABLE + ": 1",
				storage.ROLE_TABLE + ": 3",
			},
		},
		{
			name:          "query error",
			workspace:     "",
			workspaceName: "default",
			counts:        map[string]int{},
			queryError:    errors.New("database error"),
			expectError:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := storageMocks.NewMockStorage(t)

			// Mock Count calls for all tables
			tables := []string{
				storage.ENDPOINT_TABLE,
				storage.CLUSTERS_TABLE,
				storage.MODEL_REGISTRY_TABLE,
				storage.IMAGE_REGISTRY_TABLE,
				storage.MODEL_CATALOG_TABLE,
				storage.ROLE_TABLE,
				storage.API_KEY_TABLE,
			}

			for _, table := range tables {
				count := tt.counts[table]
				mockStorage.On("Count",
					table,
					[]storage.Filter{
						{Column: "metadata->>workspace", Operator: "eq", Value: tt.workspaceName},
					},
				).Return(count, tt.queryError).Maybe()

				// If there's a query error, we might not get to all tables
				if tt.queryError != nil {
					break
				}
			}

			// Mock Count call for role_assignment_table (different filter)
			if tt.queryError == nil {
				count := tt.counts[storage.ROLE_ASSIGNMENT_TABLE]
				mockStorage.On("Count",
					storage.ROLE_ASSIGNMENT_TABLE,
					[]storage.Filter{
						{Column: "spec->>workspace", Operator: "eq", Value: tt.workspaceName},
					},
				).Return(count, tt.queryError)
			}

			validator := validateWorkspaceDeletion(mockStorage)
			err := validator(tt.workspace, tt.workspaceName)

			if tt.expectError {
				assert.Error(t, err)

				if tt.queryError == nil {
					deletionErr, ok := err.(*middleware.DeletionError)
					assert.True(t, ok, "error should be DeletionError")
					if ok {
						assert.Equal(t, tt.expectedCode, deletionErr.Code)
						for _, hint := range tt.expectedHints {
							assert.Contains(t, deletionErr.Hint, hint)
						}
					}
				}
			} else {
				assert.NoError(t, err)
			}

			mockStorage.AssertExpectations(t)
		})
	}
}
