package orchestrator

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestOrchestrator_getRelateImageRegistry(t *testing.T) {
	testImageRegistry := v1.ImageRegistry{
		ID: 1,
		Metadata: &v1.Metadata{
			Name: "test",
		},
		Spec: &v1.ImageRegistrySpec{
			AuthConfig: v1.ImageRegistryAuthConfig{
				Username: "test",
				Password: "test",
			},
			URL:        "test",
			Repository: "neutree",
		},
	}

	testCluster := &v1.Cluster{
		ID: 1,
		Metadata: &v1.Metadata{
			Name: "test",
		},
		Spec: &v1.ClusterSpec{
			ImageRegistry: "test",
			Version:       "test",
		},
	}

	tests := []struct {
		name      string
		input     *v1.Cluster
		mockSetup func(*storagemocks.MockStorage)
		wantErr   bool
	}{
		{
			name:  "get relate image registry error",
			input: testCluster,
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("ListImageRegistry", mock.Anything).Return(nil, assert.AnError)
			},
			wantErr: true,
		},
		{
			name:  "get relate image registry not found",
			input: testCluster,
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("ListImageRegistry", mock.Anything).Return(nil, nil)
			},
			wantErr: true,
		},
		{
			name:  "get relate image registry success",
			input: testCluster,
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{testImageRegistry}, nil)
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storage := new(storagemocks.MockStorage)
			tt.mockSetup(storage)
			v, err := getRelateImageRegistry(storage, tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, testImageRegistry.ID, v.ID)
			}

			storage.AssertExpectations(t)
		})
	}
}
