package controllers

import (
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	v1 "github.com/neutree-ai/neutree/api/v1"
	registrymocks "github.com/neutree-ai/neutree/internal/registry/mocks"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func newTestImageRegistryController(storage *storagemocks.MockStorage, svc *registrymocks.MockImageService) *ImageRegistryController {
	return &ImageRegistryController{
		storage:      storage,
		imageService: svc,
	}
}

func TestImageRegistryControllerConnectImageRegistryPassesHTTPRegistryScheme(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		useHTTP bool
	}{
		{name: "explicit HTTP", url: "http://registry.example.com:5000", useHTTP: true},
		{name: "explicit HTTPS", url: "https://registry.example.com:5000"},
		{name: "URL without scheme", url: "registry.example.com:5000"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			imageService := registrymocks.NewMockImageService(t)
			imageService.
				On("CheckPullPermission", "registry.example.com:5000/neutree/neutree-serve", mock.Anything, tt.useHTTP).
				Return(true, nil)

			controller := newTestImageRegistryController(storagemocks.NewMockStorage(t), imageService)
			err := controller.connectImageRegistry(&v1.ImageRegistry{
				Metadata: &v1.Metadata{Name: "test"},
				Spec:     &v1.ImageRegistrySpec{URL: tt.url},
			})

			assert.NoError(t, err)
			imageService.AssertExpectations(t)
		})
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
				Repository: "",
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
				Repository: "",
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
			name:  "Pending/NoStatus -> Connected (check pull permission success)",
			input: testImageRegistry(),
			mockSetup: func(input *v1.ImageRegistry, s *storagemocks.MockStorage, imageSvc *registrymocks.MockImageService) {
				imageSvc.On("CheckPullPermission", mock.Anything, mock.Anything, true).Run(func(args mock.Arguments) {
					image := args.Get(0).(string)
					assert.Equal(t, "test/neutree/neutree-serve", image)
					arg := args.Get(1).(authn.Authenticator)
					authConfig, _ := arg.Authorization()
					assert.Equal(t, input.Spec.AuthConfig.Username, authConfig.Username)
					assert.Equal(t, input.Spec.AuthConfig.Password, authConfig.Password)
				}).Return(true, nil)
				s.On("UpdateImageRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
					arg := args.Get(1).(*v1.ImageRegistry)
					assert.Equal(t, v1.ImageRegistryPhaseCONNECTED, arg.Status.Phase)
				}).Return(nil)
			},
			wantErr: false,
		},
		{
			name:  "Pending/NoStatus -> Failed (check pull permission failed)",
			input: testImageRegistry(),
			mockSetup: func(input *v1.ImageRegistry, s *storagemocks.MockStorage, imageSvc *registrymocks.MockImageService) {
				imageSvc.On("CheckPullPermission", mock.Anything, mock.Anything, true).Run(func(args mock.Arguments) {
					image := args.Get(0).(string)
					assert.Equal(t, "test/neutree/neutree-serve", image)
					arg := args.Get(1).(authn.Authenticator)
					authConfig, _ := arg.Authorization()
					assert.Equal(t, input.Spec.AuthConfig.Username, authConfig.Username)
					assert.Equal(t, input.Spec.AuthConfig.Password, authConfig.Password)
				}).Return(false, assert.AnError)
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
				Repository: "",
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
				Repository: "",
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
			name:  "Connected -> Connected (check pull permission success)",
			input: testImageRegistry(),
			mockSetup: func(input *v1.ImageRegistry, s *storagemocks.MockStorage, imageSvc *registrymocks.MockImageService) {
				imageSvc.On("CheckPullPermission", mock.Anything, mock.Anything, true).Run(func(args mock.Arguments) {
					image := args.Get(0).(string)
					assert.Equal(t, "test/neutree/neutree-serve", image)
					arg := args.Get(1).(authn.Authenticator)
					authConfig, _ := arg.Authorization()
					assert.Equal(t, input.Spec.AuthConfig.Username, authConfig.Username)
					assert.Equal(t, input.Spec.AuthConfig.Password, authConfig.Password)
				}).Return(true, nil)
			},
			wantErr: false,
		},
		{
			name:  "Connected -> Failed (check pull permission failed)",
			input: testImageRegistry(),
			mockSetup: func(input *v1.ImageRegistry, s *storagemocks.MockStorage, imageSvc *registrymocks.MockImageService) {
				imageSvc.On("CheckPullPermission", mock.Anything, mock.Anything, true).Run(func(args mock.Arguments) {
					image := args.Get(0).(string)
					assert.Equal(t, "test/neutree/neutree-serve", image)
					arg := args.Get(1).(authn.Authenticator)
					authConfig, _ := arg.Authorization()
					assert.Equal(t, input.Spec.AuthConfig.Username, authConfig.Username)
					assert.Equal(t, input.Spec.AuthConfig.Password, authConfig.Password)
				}).Return(false, assert.AnError)
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
				Repository: "",
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
				Repository: "",
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
			name:  "Failed -> Connected (check pull permission success)",
			input: testImageRegistry(),
			mockSetup: func(input *v1.ImageRegistry, s *storagemocks.MockStorage, imageSvc *registrymocks.MockImageService) {
				imageSvc.On("CheckPullPermission", mock.Anything, mock.Anything, true).Run(func(args mock.Arguments) {
					image := args.Get(0).(string)
					assert.Equal(t, "test/neutree/neutree-serve", image)
					arg := args.Get(1).(authn.Authenticator)
					authConfig, _ := arg.Authorization()
					assert.Equal(t, input.Spec.AuthConfig.Username, authConfig.Username)
					assert.Equal(t, input.Spec.AuthConfig.Password, authConfig.Password)
				}).Return(true, nil)
				s.On("UpdateImageRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
					arg := args.Get(1).(*v1.ImageRegistry)
					assert.Equal(t, v1.ImageRegistryPhaseCONNECTED, arg.Status.Phase)
				}).Return(nil)
			},
			wantErr: false,
		},
		{
			name:  "Failed -> Failed (check pull permission failed)",
			input: testImageRegistry(),
			mockSetup: func(input *v1.ImageRegistry, s *storagemocks.MockStorage, imageSvc *registrymocks.MockImageService) {
				imageSvc.On("CheckPullPermission", mock.Anything, mock.Anything, true).Run(func(args mock.Arguments) {
					image := args.Get(0).(string)
					assert.Equal(t, "test/neutree/neutree-serve", image)
					arg := args.Get(1).(authn.Authenticator)
					authConfig, _ := arg.Authorization()
					assert.Equal(t, input.Spec.AuthConfig.Username, authConfig.Username)
					assert.Equal(t, input.Spec.AuthConfig.Password, authConfig.Password)
				}).Return(false, assert.AnError)
				// Defer block updates status to FAILED when connection fails
				s.On("UpdateImageRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
					arg := args.Get(1).(*v1.ImageRegistry)
					assert.Equal(t, v1.ImageRegistryPhaseFAILED, arg.Status.Phase)
				}).Return(nil)
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

func TestImageRegistryController_Reconcile(t *testing.T) {
	tests := []struct {
		name      string
		input     interface{}
		mockSetup func(*storagemocks.MockStorage)
		wantErr   bool
	}{
		{
			name: "reconcile success",
			input: &v1.ImageRegistry{
				Metadata: &v1.Metadata{Name: "test"},
			},
			mockSetup: func(s *storagemocks.MockStorage) {
			},
			wantErr: false,
		},
		{
			name:    "invalid key type",
			input:   "invalid",
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

func TestImageRegistryController_DetectCapabilities(t *testing.T) {
	registryWith := func(capability v1.ListRepositoriesCapability) *v1.ImageRegistry {
		obj := &v1.ImageRegistry{
			Metadata: &v1.Metadata{Name: "test", Workspace: "default"},
			Spec:     &v1.ImageRegistrySpec{URL: "https://registry.example.com", Repository: "team"},
		}

		if capability != "" {
			obj.Status = &v1.ImageRegistryStatus{
				Capabilities: &v1.ImageRegistryCapabilities{ListRepositories: capability},
			}
		}

		return obj
	}

	t.Run("records what the registry turned out to support", func(t *testing.T) {
		// Established here rather than when a user is waiting on a listing.
		repositoryService := registrymocks.NewMockRepositoryService(t)
		repositoryService.
			On("DetectListRepositoriesCapability", mock.Anything).
			Return(v1.ListRepositoriesHarborProjects, nil)

		controller := &ImageRegistryController{repositoryService: repositoryService}
		capabilities := controller.detectCapabilities(registryWith(""))

		assert.Equal(t, v1.ListRepositoriesHarborProjects, capabilities.ListRepositories)
	})

	t.Run("re-establishes it every reconcile", func(t *testing.T) {
		// Credentials get rotated and permissions get changed, so a recorded
		// capability is never taken as still true.
		repositoryService := registrymocks.NewMockRepositoryService(t)
		repositoryService.
			On("DetectListRepositoriesCapability", mock.Anything).
			Return(v1.ListRepositoriesHarborProjects, nil)

		controller := &ImageRegistryController{repositoryService: repositoryService}
		capabilities := controller.detectCapabilities(registryWith(v1.ListRepositoriesUnauthorized))

		assert.Equal(t, v1.ListRepositoriesHarborProjects, capabilities.ListRepositories)
	})

	t.Run("keeps what is known when the registry says nothing", func(t *testing.T) {
		// A timeout is not an answer about what a registry supports. Writing
		// "unsupported" here would take browsing away from a working registry
		// after one bad minute, and leave it away until somebody noticed.
		repositoryService := registrymocks.NewMockRepositoryService(t)
		repositoryService.
			On("DetectListRepositoriesCapability", mock.Anything).
			Return(v1.ListRepositoriesCapability(""), errors.New("dial tcp: i/o timeout"))

		controller := &ImageRegistryController{repositoryService: repositoryService}
		capabilities := controller.detectCapabilities(registryWith(v1.ListRepositoriesHarborProjects))

		assert.Equal(t, v1.ListRepositoriesHarborProjects, capabilities.ListRepositories)
	})

	t.Run("leaves it unestablished when nothing was ever established", func(t *testing.T) {
		repositoryService := registrymocks.NewMockRepositoryService(t)
		repositoryService.
			On("DetectListRepositoriesCapability", mock.Anything).
			Return(v1.ListRepositoriesCapability(""), errors.New("dial tcp: i/o timeout"))

		controller := &ImageRegistryController{repositoryService: repositoryService}

		assert.Nil(t, controller.detectCapabilities(registryWith("")))
	})
}
