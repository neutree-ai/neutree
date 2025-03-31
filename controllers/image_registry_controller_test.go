package controllers

import (
	"testing"
	"time"

	"github.com/docker/docker/api/types/registry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"k8s.io/client-go/util/workqueue"

	v1 "github.com/neutree-ai/neutree/api/v1"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
	commonmocks "github.com/neutree-ai/neutree/testing/mocks"
)

func TestImageRegistryController_Sync(t *testing.T) {
	now := time.Now().Format(time.RFC3339Nano)

	tests := []struct {
		name      string
		input     *v1.ImageRegistry
		mockSetup func(*storagemocks.MockStorage, *commonmocks.MockAPIClient)
		wantErr   bool
	}{
		{
			name: "delete image registry from storage",
			input: &v1.ImageRegistry{
				ID:       1,
				Metadata: v1.Metadata{DeletionTimestamp: now},
				Status:   v1.ImageRegistryStatus{Phase: v1.ImageRegistryPhaseDELETED},
			},
			mockSetup: func(s *storagemocks.MockStorage, d *commonmocks.MockAPIClient) {
				s.On("DeleteImageRegistry", "1").Return(nil)
			},
			wantErr: false,
		},
		{
			name: "delete image registry from storage failed",
			input: &v1.ImageRegistry{
				ID:       1,
				Metadata: v1.Metadata{DeletionTimestamp: now},
				Status:   v1.ImageRegistryStatus{Phase: v1.ImageRegistryPhaseDELETED},
			},
			mockSetup: func(s *storagemocks.MockStorage, d *commonmocks.MockAPIClient) {
				s.On("DeleteImageRegistry", "1").Return(assert.AnError)
			},
			wantErr: true,
		},
		{
			name: "set image registry deleted status when image registry deletionTimestamp is not empty",
			input: &v1.ImageRegistry{
				ID:       1,
				Metadata: v1.Metadata{DeletionTimestamp: now},
			},
			mockSetup: func(s *storagemocks.MockStorage, d *commonmocks.MockAPIClient) {
				s.On("UpdateImageRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
					arg := args.Get(1).(*v1.ImageRegistry)
					assert.Equal(t, v1.ImageRegistryPhaseDELETED, arg.Status.Phase)
				}).Return(nil)
			},
			wantErr: false,
		},
		{
			name: "set image registry deleted status failed due to storage error",
			input: &v1.ImageRegistry{
				ID:       1,
				Metadata: v1.Metadata{DeletionTimestamp: now},
			},
			mockSetup: func(s *storagemocks.MockStorage, d *commonmocks.MockAPIClient) {
				s.On("UpdateImageRegistry", "1", mock.Anything).Return(assert.AnError)
			},
			wantErr: true,
		},
		{
			name: "connect image registry successfully",
			input: &v1.ImageRegistry{
				ID: 1,
				Spec: v1.ImageRegistrySpec{
					AuthConfig: v1.ImageRegistryAuthConfig{
						Username: "test",
						Password: "test",
					},
					URL: "http://test",
				},
			},
			mockSetup: func(s *storagemocks.MockStorage, d *commonmocks.MockAPIClient) {
				d.On("RegistryLogin", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					arg := args.Get(1).(registry.AuthConfig)
					assert.Equal(t, "test", arg.Username)
					assert.Equal(t, "test", arg.Password)
					assert.Equal(t, "http://test", arg.ServerAddress)
				}).Return(registry.AuthenticateOKBody{}, nil)
				s.On("UpdateImageRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
					arg := args.Get(1).(*v1.ImageRegistry)
					assert.Equal(t, v1.ImageRegistryPhaseCONNECTED, arg.Status.Phase)
				}).Return(nil)
			},
			wantErr: false,
		},
		{
			name: "connect image registry failed",
			input: &v1.ImageRegistry{
				ID: 1,
				Spec: v1.ImageRegistrySpec{
					AuthConfig: v1.ImageRegistryAuthConfig{
						Username: "test",
						Password: "test",
					},
					URL: "http://test",
				},
			},
			mockSetup: func(s *storagemocks.MockStorage, d *commonmocks.MockAPIClient) {
				d.On("RegistryLogin", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					arg := args.Get(1).(registry.AuthConfig)
					assert.Equal(t, "test", arg.Username)
					assert.Equal(t, "test", arg.Password)
					assert.Equal(t, "http://test", arg.ServerAddress)
				}).Return(registry.AuthenticateOKBody{}, assert.AnError)
				s.On("UpdateImageRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
					arg := args.Get(1).(*v1.ImageRegistry)
					assert.Equal(t, v1.ImageRegistryPhaseFAILED, arg.Status.Phase)
				}).Return(nil)
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			mockDocker := &commonmocks.MockAPIClient{}
			tt.mockSetup(mockStorage, mockDocker)

			c := &ImageRegistryController{
				storage:      mockStorage,
				dockerClient: mockDocker,
				queue:        workqueue.NewRateLimitingQueueWithConfig(workqueue.DefaultControllerRateLimiter(), workqueue.RateLimitingQueueConfig{Name: "test"}),
			}

			err := c.sync(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			mockStorage.AssertExpectations(t)
		})
	}
}
