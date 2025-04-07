package controllers

import (
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"k8s.io/client-go/util/workqueue"

	v1 "github.com/neutree-ai/neutree/api/v1"
	registrymocks "github.com/neutree-ai/neutree/internal/registry/mocks"
	"github.com/neutree-ai/neutree/pkg/storage"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func newTestImageRegistryController(storage *storagemocks.MockStorage, svc *registrymocks.MockImageService) *ImageRegistryController {
	return &ImageRegistryController{
		storage:      storage,
		imageService: svc,
		baseController: &BaseController{
			queue:        workqueue.NewRateLimitingQueueWithConfig(workqueue.DefaultControllerRateLimiter(), workqueue.RateLimitingQueueConfig{Name: "model-registry"}),
			workers:      1,
			syncInterval: time.Second * 10,
		},
	}
}

func TestImageRegistryController_Sync_Delete(t *testing.T) {
	now := time.Now().Format(time.RFC3339Nano)

	testImageRegistry := func() *v1.ImageRegistry {
		return &v1.ImageRegistry{
			ID: 1,
			Metadata: &v1.Metadata{
				Name:              "test",
				DeletionTimestamp: now,
			},
			Status: &v1.ImageRegistryStatus{Phase: v1.ImageRegistryPhaseDELETED},
		}
	}

	tests := []struct {
		name      string
		input     *v1.ImageRegistry
		mockSetup func(*storagemocks.MockStorage, *registrymocks.MockImageService)
		wantErr   bool
	}{
		{
			name:  "Deleted -> Deleted (storage delete success)",
			input: testImageRegistry(),
			mockSetup: func(s *storagemocks.MockStorage, imageSvc *registrymocks.MockImageService) {
				s.On("DeleteImageRegistry", "1").Return(nil)
			},
			wantErr: false,
		},
		{
			name:  "Deleted -> Deleted (storage delete failed)",
			input: testImageRegistry(),
			mockSetup: func(s *storagemocks.MockStorage, imageSvc *registrymocks.MockImageService) {
				s.On("DeleteImageRegistry", "1").Return(assert.AnError)
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			mockImageService := &registrymocks.MockImageService{}
			tt.mockSetup(mockStorage, mockImageService)

			c := newTestImageRegistryController(mockStorage, mockImageService)

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

func TestImageRegistryController_Sync_PendingOrNoStatus(t *testing.T) {
	testImageRegistry := func() *v1.ImageRegistry {
		return &v1.ImageRegistry{
			ID: 1,
			Metadata: &v1.Metadata{
				Name: "test",
			},
			Spec: &v1.ImageRegistrySpec{
				AuthConfig: v1.ImageRegistryAuthConfig{
					Username: "test",
					Password: "test",
				},
				Repository: "neutree",
				URL:        "http://test",
			},
		}
	}

	testImageRegistryWithDeletionTimestamp := func() *v1.ImageRegistry {
		now := time.Now().Format(time.RFC3339Nano)
		return &v1.ImageRegistry{
			ID: 1,
			Metadata: &v1.Metadata{
				Name:              "test",
				DeletionTimestamp: now,
			},
			Spec: &v1.ImageRegistrySpec{
				AuthConfig: v1.ImageRegistryAuthConfig{
					Username: "test",
					Password: "test",
				},
				Repository: "neutree",
				URL:        "http://test",
			},
		}
	}

	tests := []struct {
		name      string
		input     *v1.ImageRegistry
		mockSetup func(*v1.ImageRegistry, *storagemocks.MockStorage, *registrymocks.MockImageService)
		wantErr   bool
	}{
		{
			name:  "Pending/NoStatus -> Connected (image service list tags success)",
			input: testImageRegistry(),
			mockSetup: func(input *v1.ImageRegistry, s *storagemocks.MockStorage, imageSvc *registrymocks.MockImageService) {
				imageSvc.On("ListImageTags", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					image := args.Get(0).(string)
					assert.Equal(t, "test/neutree/neutree-serve", image)
					arg := args.Get(1).(authn.Authenticator)
					authConfig, _ := arg.Authorization()
					assert.Equal(t, input.Spec.AuthConfig.Username, authConfig.Username)
					assert.Equal(t, input.Spec.AuthConfig.Password, authConfig.Password)
				}).Return(nil, nil)
				s.On("UpdateImageRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
					arg := args.Get(1).(*v1.ImageRegistry)
					assert.Equal(t, v1.ImageRegistryPhaseCONNECTED, arg.Status.Phase)
				}).Return(nil)
			},
			wantErr: false,
		},
		{
			name:  "Pending/NoStatus -> Failed (image service list tags failed)",
			input: testImageRegistry(),
			mockSetup: func(input *v1.ImageRegistry, s *storagemocks.MockStorage, imageSvc *registrymocks.MockImageService) {
				imageSvc.On("ListImageTags", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					image := args.Get(0).(string)
					assert.Equal(t, "test/neutree/neutree-serve", image)
					arg := args.Get(1).(authn.Authenticator)
					authConfig, _ := arg.Authorization()
					assert.Equal(t, input.Spec.AuthConfig.Username, authConfig.Username)
					assert.Equal(t, input.Spec.AuthConfig.Password, authConfig.Password)
				}).Return(nil, assert.AnError)
				s.On("UpdateImageRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
					arg := args.Get(1).(*v1.ImageRegistry)
					assert.Equal(t, v1.ImageRegistryPhaseFAILED, arg.Status.Phase)
				}).Return(nil)
			},
			wantErr: true,
		},
		{
			name:  "Pending/NoStatus -> Deleted",
			input: testImageRegistryWithDeletionTimestamp(),
			mockSetup: func(input *v1.ImageRegistry, s *storagemocks.MockStorage, imageSvc *registrymocks.MockImageService) {
				s.On("UpdateImageRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
					arg := args.Get(1).(*v1.ImageRegistry)
					assert.Equal(t, v1.ImageRegistryPhaseDELETED, arg.Status.Phase)
				}).Return(nil)
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			mockImageService := &registrymocks.MockImageService{}
			tt.mockSetup(tt.input, mockStorage, mockImageService)

			c := newTestImageRegistryController(mockStorage, mockImageService)

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

func TestImageRegistryController_Sync_Conneted(t *testing.T) {
	testImageRegistry := func() *v1.ImageRegistry {
		return &v1.ImageRegistry{
			ID: 1,
			Metadata: &v1.Metadata{
				Name: "test",
			},
			Spec: &v1.ImageRegistrySpec{
				AuthConfig: v1.ImageRegistryAuthConfig{
					Username: "test",
					Password: "test",
				},
				URL:        "http://test",
				Repository: "neutree",
			},
			Status: &v1.ImageRegistryStatus{Phase: v1.ImageRegistryPhaseCONNECTED},
		}
	}

	testImageRegistryWithDeletionTimestamp := func() *v1.ImageRegistry {
		return &v1.ImageRegistry{
			ID: 1,
			Metadata: &v1.Metadata{
				Name:              "test",
				DeletionTimestamp: time.Now().Format(time.RFC3339Nano),
			},
			Spec: &v1.ImageRegistrySpec{
				AuthConfig: v1.ImageRegistryAuthConfig{
					Username: "test",
					Password: "test",
				},
				URL:        "http://test",
				Repository: "neutree",
			},
			Status: &v1.ImageRegistryStatus{Phase: v1.ImageRegistryPhaseCONNECTED},
		}
	}

	tests := []struct {
		name      string
		input     *v1.ImageRegistry
		mockSetup func(*v1.ImageRegistry, *storagemocks.MockStorage, *registrymocks.MockImageService)
		wantErr   bool
	}{
		{
			name:  "Connected -> Connected (image service list tags success)",
			input: testImageRegistry(),
			mockSetup: func(input *v1.ImageRegistry, s *storagemocks.MockStorage, imageSvc *registrymocks.MockImageService) {
				imageSvc.On("ListImageTags", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					image := args.Get(0).(string)
					assert.Equal(t, "test/neutree/neutree-serve", image)
					arg := args.Get(1).(authn.Authenticator)
					authConfig, _ := arg.Authorization()
					assert.Equal(t, input.Spec.AuthConfig.Username, authConfig.Username)
					assert.Equal(t, input.Spec.AuthConfig.Password, authConfig.Password)
				}).Return(nil, nil)
			},
			wantErr: false,
		},
		{
			name:  "Connected -> Failed (image service list tags failed)",
			input: testImageRegistry(),
			mockSetup: func(input *v1.ImageRegistry, s *storagemocks.MockStorage, imageSvc *registrymocks.MockImageService) {
				imageSvc.On("ListImageTags", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					image := args.Get(0).(string)
					assert.Equal(t, "test/neutree/neutree-serve", image)
					arg := args.Get(1).(authn.Authenticator)
					authConfig, _ := arg.Authorization()
					assert.Equal(t, input.Spec.AuthConfig.Username, authConfig.Username)
					assert.Equal(t, input.Spec.AuthConfig.Password, authConfig.Password)
				}).Return(nil, assert.AnError)
				s.On("UpdateImageRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
					arg := args.Get(1).(*v1.ImageRegistry)
					assert.Equal(t, v1.ImageRegistryPhaseFAILED, arg.Status.Phase)
				}).Return(nil)
			},
			wantErr: true,
		},
		{
			name:  "Connected -> Deleted",
			input: testImageRegistryWithDeletionTimestamp(),
			mockSetup: func(input *v1.ImageRegistry, s *storagemocks.MockStorage, imageSvc *registrymocks.MockImageService) {
				s.On("UpdateImageRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
					arg := args.Get(1).(*v1.ImageRegistry)
					assert.Equal(t, v1.ImageRegistryPhaseDELETED, arg.Status.Phase)
				}).Return(nil)
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			mockImageService := &registrymocks.MockImageService{}
			tt.mockSetup(tt.input, mockStorage, mockImageService)

			c := newTestImageRegistryController(mockStorage, mockImageService)

			err := c.sync(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			mockStorage.AssertExpectations(t)
			mockImageService.AssertExpectations(t)
		})
	}
}

func TestImageRegistryController_Sync_Failed(t *testing.T) {
	testImageRegistry := func() *v1.ImageRegistry {
		return &v1.ImageRegistry{
			ID: 1,
			Metadata: &v1.Metadata{
				Name: "test",
			},
			Spec: &v1.ImageRegistrySpec{
				AuthConfig: v1.ImageRegistryAuthConfig{
					Username: "test",
					Password: "test",
				},
				Repository: "neutree",
				URL:        "http://test",
			},
			Status: &v1.ImageRegistryStatus{Phase: v1.ImageRegistryPhaseFAILED},
		}
	}

	testImageRegistryWithDeletionTimestamp := func() *v1.ImageRegistry {
		return &v1.ImageRegistry{
			ID: 1,
			Metadata: &v1.Metadata{
				Name:              "test",
				DeletionTimestamp: time.Now().Format(time.RFC3339Nano),
			},
			Spec: &v1.ImageRegistrySpec{
				AuthConfig: v1.ImageRegistryAuthConfig{
					Username: "test",
					Password: "test",
				},
				Repository: "neutree",
				URL:        "http://test",
			},
			Status: &v1.ImageRegistryStatus{Phase: v1.ImageRegistryPhaseFAILED},
		}
	}

	tests := []struct {
		name      string
		input     *v1.ImageRegistry
		mockSetup func(*v1.ImageRegistry, *storagemocks.MockStorage, *registrymocks.MockImageService)
		wantErr   bool
	}{
		{
			name:  "Failed -> Connected (image service list tags success)",
			input: testImageRegistry(),
			mockSetup: func(input *v1.ImageRegistry, s *storagemocks.MockStorage, imageSvc *registrymocks.MockImageService) {
				imageSvc.On("ListImageTags", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					image := args.Get(0).(string)
					assert.Equal(t, "test/neutree/neutree-serve", image)
					arg := args.Get(1).(authn.Authenticator)
					authConfig, _ := arg.Authorization()
					assert.Equal(t, input.Spec.AuthConfig.Username, authConfig.Username)
					assert.Equal(t, input.Spec.AuthConfig.Password, authConfig.Password)
				}).Return(nil, nil)
				s.On("UpdateImageRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
					arg := args.Get(1).(*v1.ImageRegistry)
					assert.Equal(t, v1.ImageRegistryPhaseCONNECTED, arg.Status.Phase)
				}).Return(nil)
			},
			wantErr: false,
		},
		{
			name:  "Failed -> Failed (image service list tags failed)",
			input: testImageRegistry(),
			mockSetup: func(input *v1.ImageRegistry, s *storagemocks.MockStorage, imageSvc *registrymocks.MockImageService) {
				imageSvc.On("ListImageTags", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					image := args.Get(0).(string)
					assert.Equal(t, "test/neutree/neutree-serve", image)
					arg := args.Get(1).(authn.Authenticator)
					authConfig, _ := arg.Authorization()
					assert.Equal(t, input.Spec.AuthConfig.Username, authConfig.Username)
					assert.Equal(t, input.Spec.AuthConfig.Password, authConfig.Password)
				}).Return(nil, assert.AnError)
			},
			wantErr: true,
		},
		{
			name:  "Failed -> Deleted",
			input: testImageRegistryWithDeletionTimestamp(),
			mockSetup: func(input *v1.ImageRegistry, s *storagemocks.MockStorage, imageSvc *registrymocks.MockImageService) {
				s.On("UpdateImageRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
					arg := args.Get(1).(*v1.ImageRegistry)
					assert.Equal(t, v1.ImageRegistryPhaseDELETED, arg.Status.Phase)
				}).Return(nil)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			mockImageService := &registrymocks.MockImageService{}
			tt.mockSetup(tt.input, mockStorage, mockImageService)
			c := newTestImageRegistryController(mockStorage, mockImageService)

			err := c.sync(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			mockStorage.AssertExpectations(t)
			mockImageService.AssertExpectations(t)
		})
	}
}

func TestImageRegistryController_ListKeys(t *testing.T) {
	tests := []struct {
		name      string
		mockSetup func(*storagemocks.MockStorage)
		wantErr   bool
	}{
		{
			name: "list image registry success",
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("ListImageRegistry", storage.ListOption{}).Return([]v1.ImageRegistry{{ID: 1}}, nil)
			},
			wantErr: false,
		},
		{
			name: "list image registry failed",
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("ListImageRegistry", storage.ListOption{}).Return(nil, assert.AnError)
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			tt.mockSetup(mockStorage)

			c := &ImageRegistryController{storage: mockStorage}
			keys, err := c.ListKeys()

			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, keys)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, 1, len(keys))
				assert.Equal(t, 1, keys[0])
			}
			mockStorage.AssertExpectations(t)
		})
	}
}

func TestImageRegistryController_Reconcile(t *testing.T) {
	tests := []struct {
		name      string
		input     interface{}
		mockSetup func(*storagemocks.MockStorage)
		wantErr   bool
	}{
		{
			name:  "reconcile success",
			input: 1,
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("GetImageRegistry", "1").Return(&v1.ImageRegistry{
					Metadata: &v1.Metadata{Name: "test"},
				}, nil)
			},
			wantErr: false,
		},
		{
			name:    "invalid key type",
			input:   "invalid",
			wantErr: true,
		},
		{
			name:  "get registry error",
			input: 1,
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("GetImageRegistry", "1").Return(nil, assert.AnError)
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			if tt.mockSetup != nil {
				tt.mockSetup(mockStorage)
			}

			c := &ImageRegistryController{storage: mockStorage, syncHandler: func(obj *v1.ImageRegistry) error { return nil }}
			err := c.Reconcile(tt.input)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			mockStorage.AssertExpectations(t)
		})
	}
}
