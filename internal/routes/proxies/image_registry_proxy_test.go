package proxies

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/pkg/storage"
	storageMocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func TestValidateImageRegistryDeletion(t *testing.T) {
	tests := []struct {
		name          string
		workspace     string
		registryName  string
		clusterCount  int
		queryError    error
		expectError   bool
		expectedCode  string
		expectedHint  string
	}{
		{
			name:         "no dependencies - deletion allowed",
			workspace:    "default",
			registryName: "my-registry",
			clusterCount: 0,
			queryError:   nil,
			expectError:  false,
		},
		{
			name:         "has dependencies - deletion blocked",
			workspace:    "default",
			registryName: "my-registry",
			clusterCount: 3,
			queryError:   nil,
			expectError:  true,
			expectedCode: "10127",
			expectedHint: "3 cluster(s) still reference this image registry",
		},
		{
			name:         "query error",
			workspace:    "default",
			registryName: "my-registry",
			clusterCount: 0,
			queryError:   errors.New("database error"),
			expectError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := storageMocks.NewMockStorage(t)

			mockStorage.On("Count",
				storage.CLUSTERS_TABLE,
				[]storage.Filter{
					{Column: "metadata->>workspace", Operator: "eq", Value: tt.workspace},
					{Column: "spec->>image_registry", Operator: "eq", Value: tt.registryName},
				},
			).Return(tt.clusterCount, tt.queryError)

			validator := validateImageRegistryDeletion(mockStorage)
			err := validator(tt.workspace, tt.registryName)

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
