package controllers

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"k8s.io/client-go/util/workqueue"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/model_registry"
	modelregistrymocks "github.com/neutree-ai/neutree/pkg/model_registry/mocks"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func TestModelRegistryController_Sync(t *testing.T) {
	now := time.Now().Format(time.RFC3339Nano)

	tests := []struct {
		name      string
		input     *v1.ModelRegistry
		mockSetup func(*storagemocks.MockStorage, *modelregistrymocks.MockModelRegistry)
		wantErr   bool
	}{
		{
			name: "delete model registry from storage",
			input: &v1.ModelRegistry{
				ID:       1,
				Metadata: v1.Metadata{DeletionTimestamp: now},
				Status:   v1.ModelRegistryStatus{Phase: v1.ModelRegistryPhaseDELETED},
			},
			mockSetup: func(s *storagemocks.MockStorage, m *modelregistrymocks.MockModelRegistry) {
				s.On("DeleteModelRegistry", "1").Return(nil)
			},
		},
		{
			name: "disconnect model registry when deletionTimestamp is not empty",
			input: &v1.ModelRegistry{
				ID:       1,
				Metadata: v1.Metadata{DeletionTimestamp: now},
			},
			mockSetup: func(s *storagemocks.MockStorage, m *modelregistrymocks.MockModelRegistry) {
				s.On("UpdateModelRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.ModelRegistry)
					assert.Equal(t, v1.ModelRegistryPhaseDELETED, obj.Status.Phase)
				}).Return(nil)
				m.On("Disconnect").Return(nil)
			},
		},
		{
			name: "connect model registry success",
			input: &v1.ModelRegistry{
				ID:     1,
				Status: v1.ModelRegistryStatus{Phase: v1.ModelRegistryPhasePENDING},
			},
			mockSetup: func(s *storagemocks.MockStorage, m *modelregistrymocks.MockModelRegistry) {
				m.On("Connect").Return(nil)
				s.On("UpdateModelRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.ModelRegistry)
					assert.Equal(t, v1.ModelRegistryPhaseCONNECTED, obj.Status.Phase)
				}).Return(nil)
			},
		},
		{
			name: "connect model registry failed",
			input: &v1.ModelRegistry{
				ID:     1,
				Status: v1.ModelRegistryStatus{Phase: v1.ModelRegistryPhasePENDING},
			},
			mockSetup: func(s *storagemocks.MockStorage, m *modelregistrymocks.MockModelRegistry) {
				m.On("Connect").Return(assert.AnError)
				s.On("UpdateModelRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.ModelRegistry)
					assert.Equal(t, v1.ModelRegistryPhaseFAILED, obj.Status.Phase)
				}).Return(nil)
			},
			wantErr: true,
		},
		{
			name: "health check model registry failed",
			input: &v1.ModelRegistry{
				ID:     1,
				Status: v1.ModelRegistryStatus{Phase: v1.ModelRegistryPhaseCONNECTED},
			},
			mockSetup: func(s *storagemocks.MockStorage, m *modelregistrymocks.MockModelRegistry) {
				m.On("HealthyCheck").Return(false)
				s.On("UpdateModelRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.ModelRegistry)
					assert.Equal(t, v1.ModelRegistryPhaseFAILED, obj.Status.Phase)
				}).Return(nil)
			},
			wantErr: true,
		},
		{
			name: "health check model registry success",
			input: &v1.ModelRegistry{
				ID:     1,
				Status: v1.ModelRegistryStatus{Phase: v1.ModelRegistryPhaseCONNECTED},
			},
			mockSetup: func(s *storagemocks.MockStorage, m *modelregistrymocks.MockModelRegistry) {
				m.On("HealthyCheck").Return(true)
			},
			wantErr: false,
		},
		{
			name: "reconnect model registry success",
			input: &v1.ModelRegistry{
				ID:     1,
				Status: v1.ModelRegistryStatus{Phase: v1.ModelRegistryPhaseFAILED},
			},
			mockSetup: func(s *storagemocks.MockStorage, m *modelregistrymocks.MockModelRegistry) {
				m.On("Disconnect").Return(nil)
				m.On("Connect").Return(nil)
				s.On("UpdateModelRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.ModelRegistry)
					assert.Equal(t, v1.ModelRegistryPhaseCONNECTED, obj.Status.Phase)
				}).Return(nil)
			},
			wantErr: false,
		},
		{
			name: "reconnect model registry failed",
			input: &v1.ModelRegistry{
				ID:     1,
				Status: v1.ModelRegistryStatus{Phase: v1.ModelRegistryPhaseFAILED},
			},
			mockSetup: func(s *storagemocks.MockStorage, m *modelregistrymocks.MockModelRegistry) {
				m.On("Disconnect").Return(nil)
				m.On("Connect").Return(assert.AnError)
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			mockModel := &modelregistrymocks.MockModelRegistry{}
			tt.mockSetup(mockStorage, mockModel)

			c := &ModelRegistryController{
				storage: mockStorage,
				newModelRegistry: func(obj *v1.ModelRegistry) (model_registry.ModelRegistry, error) {
					return mockModel, nil
				},
				queue: workqueue.NewRateLimitingQueueWithConfig(
					workqueue.DefaultControllerRateLimiter(),
					workqueue.RateLimitingQueueConfig{Name: "test"}),
			}

			err := c.sync(tt.input)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			mockStorage.AssertExpectations(t)
			mockModel.AssertExpectations(t)
		})
	}
}
