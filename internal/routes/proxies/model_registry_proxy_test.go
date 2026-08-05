package proxies

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/admission"
	"github.com/neutree-ai/neutree/pkg/storage"
	storageMocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func TestValidateModelRegistryDeletion(t *testing.T) {
	tests := []struct {
		name          string
		workspace     string
		registryName  string
		endpointCount int
		queryError    error
		expectError   bool
		expectedCode  int
		expectedHint  string
	}{
		{
			name:          "no dependencies - deletion allowed",
			workspace:     "default",
			registryName:  "my-registry",
			endpointCount: 0,
			queryError:    nil,
			expectError:   false,
		},
		{
			name:          "has dependencies - deletion blocked",
			workspace:     "default",
			registryName:  "my-registry",
			endpointCount: 2,
			queryError:    nil,
			expectError:   true,
			expectedCode:  10128,
			expectedHint:  "2 endpoint(s) still reference this model registry",
		},
		{
			name:          "query error",
			workspace:     "default",
			registryName:  "my-registry",
			endpointCount: 0,
			queryError:    errors.New("database error"),
			expectError:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := storageMocks.NewMockStorage(t)

			mockStorage.On("Count",
				storage.ENDPOINT_TABLE,
				[]storage.Filter{
					{Column: "metadata->>workspace", Operator: "eq", Value: tt.workspace},
					{Column: "spec->model->>registry", Operator: "eq", Value: tt.registryName},
				},
			).Return(tt.endpointCount, tt.queryError)

			err := validateModelRegistryDeleteDependencies(mockStorage, v1.ModelRegistry{Metadata: &v1.Metadata{Workspace: tt.workspace, Name: tt.registryName}})

			if tt.expectError {
				assert.Error(t, err)

				if tt.queryError == nil {
					var deletionErr *admission.Error
					ok := errors.As(err, &deletionErr)
					assert.True(t, ok, "error should be admission.Error")
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
