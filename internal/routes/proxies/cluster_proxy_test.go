package proxies

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/internal/registry"
	registryMocks "github.com/neutree-ai/neutree/internal/registry/mocks"
	"github.com/neutree-ai/neutree/pkg/storage"
	storageMocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func TestValidateClusterDeletion(t *testing.T) {
	tests := []struct {
		name          string
		workspace     string
		clusterName   string
		endpointCount int
		queryError    error
		expectError   bool
		expectedCode  string
		expectedHint  string
	}{
		{
			name:          "no dependencies - deletion allowed",
			workspace:     "default",
			clusterName:   "my-cluster",
			endpointCount: 0,
			queryError:    nil,
			expectError:   false,
		},
		{
			name:          "has dependencies - deletion blocked",
			workspace:     "default",
			clusterName:   "my-cluster",
			endpointCount: 5,
			queryError:    nil,
			expectError:   true,
			expectedCode:  "10126",
			expectedHint:  "5 endpoint(s) still reference this cluster",
		},
		{
			name:          "query error",
			workspace:     "default",
			clusterName:   "my-cluster",
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
					{Column: "spec->>cluster", Operator: "eq", Value: tt.clusterName},
				},
			).Return(tt.endpointCount, tt.queryError)

			validator := validateClusterDeletion(mockStorage)
			err := validator(tt.workspace, tt.clusterName)

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

func createTestContext(params gin.Params) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c.Params = params
	return c, w
}

func TestGetAvailableUpgradeVersions(t *testing.T) {
	tests := []struct {
		name               string
		workspace          string
		clusterName        string
		setupMock          func(s *storageMocks.MockStorage, imgSvc *registryMocks.MockImageService)
		expectedStatusCode int
		expectedResponse   *availableUpgradeVersionsResponse
		expectedError      string
	}{
		{
			name:        "success - filters by nvidia accelerator type and ignores unlabeled tags",
			workspace:   "default",
			clusterName: "test-cluster",
			setupMock: func(s *storageMocks.MockStorage, imgSvc *registryMocks.MockImageService) {
				nvidiaGPU := string(v1.AcceleratorTypeNVIDIAGPU)
				s.On("ListCluster", mock.Anything).Return([]v1.Cluster{
					{
						Spec: &v1.ClusterSpec{
							Type:          v1.SSHClusterType,
							Version:       "v1.0.0",
							ImageRegistry: "my-registry",
						},
						Status: &v1.ClusterStatus{
							AcceleratorType: &nvidiaGPU,
						},
					},
				}, nil)
				s.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{
					{
						Spec: &v1.ImageRegistrySpec{
							URL: "registry.example.com",
						},
					},
				}, nil)
				imgSvc.On("ListImageTags", mock.Anything, mock.Anything).Return([]string{
					"v1.0.0", "v1.0.0-rocm", "v1.0.1-rc.1", "v1.1.0", "v1.1.0-rocm",
					"v1.0.1-nightly-20260313", "latest",
				}, nil)
				// NVIDIA images
				imgSvc.On("GetImageLabels", mock.MatchedBy(func(s string) bool { return s == "registry.example.com/neutree/neutree-serve:v1.0.0" }), mock.Anything).
					Return(map[string]string{v1.ImageLabelVersion: "v1.0.0", v1.ImageLabelAcceleratorType: "nvidia_gpu"}, nil)
				imgSvc.On("GetImageLabels", mock.MatchedBy(func(s string) bool { return s == "registry.example.com/neutree/neutree-serve:v1.0.1-rc.1" }), mock.Anything).
					Return(map[string]string{v1.ImageLabelVersion: "v1.0.1-rc.1", v1.ImageLabelAcceleratorType: "nvidia_gpu"}, nil)
				imgSvc.On("GetImageLabels", mock.MatchedBy(func(s string) bool { return s == "registry.example.com/neutree/neutree-serve:v1.1.0" }), mock.Anything).
					Return(map[string]string{v1.ImageLabelVersion: "v1.1.0", v1.ImageLabelAcceleratorType: "nvidia_gpu"}, nil)
				// AMD images — should be filtered out for nvidia cluster
				imgSvc.On("GetImageLabels", mock.MatchedBy(func(s string) bool { return s == "registry.example.com/neutree/neutree-serve:v1.0.0-rocm" }), mock.Anything).
					Return(map[string]string{v1.ImageLabelVersion: "v1.0.0", v1.ImageLabelAcceleratorType: "amd_gpu"}, nil)
				imgSvc.On("GetImageLabels", mock.MatchedBy(func(s string) bool { return s == "registry.example.com/neutree/neutree-serve:v1.1.0-rocm" }), mock.Anything).
					Return(map[string]string{v1.ImageLabelVersion: "v1.1.0", v1.ImageLabelAcceleratorType: "amd_gpu"}, nil)
				// Nightly/dev — no version label, tag used as version
				imgSvc.On("GetImageLabels", mock.MatchedBy(func(s string) bool { return s == "registry.example.com/neutree/neutree-serve:v1.0.1-nightly-20260313" }), mock.Anything).
					Return(map[string]string{}, nil)
				// "latest" is not valid semver, will be skipped
				imgSvc.On("GetImageLabels", mock.MatchedBy(func(s string) bool { return s == "registry.example.com/neutree/neutree-serve:latest" }), mock.Anything).
					Return(map[string]string{}, nil)
			},
			expectedStatusCode: http.StatusOK,
			expectedResponse: &availableUpgradeVersionsResponse{
				CurrentVersion:    "v1.0.0",
				AvailableVersions: []string{"v1.0.0", "v1.0.1-nightly-20260313", "v1.0.1-rc.1", "v1.1.0"},
			},
		},
		{
			name:        "success - defaults to nvidia_gpu when accelerator type is empty",
			workspace:   "default",
			clusterName: "test-cluster",
			setupMock: func(s *storageMocks.MockStorage, imgSvc *registryMocks.MockImageService) {
				s.On("ListCluster", mock.Anything).Return([]v1.Cluster{
					{
						Spec: &v1.ClusterSpec{
							Type:          v1.SSHClusterType,
							Version:       "v1.0.0",
							ImageRegistry: "my-registry",
						},
						// No status / no accelerator type → defaults to nvidia_gpu
					},
				}, nil)
				s.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{
					{
						Spec: &v1.ImageRegistrySpec{
							URL: "registry.example.com",
						},
					},
				}, nil)
				imgSvc.On("ListImageTags", mock.Anything, mock.Anything).Return([]string{
					"v1.0.0", "v1.0.0-rocm",
				}, nil)
				imgSvc.On("GetImageLabels", mock.MatchedBy(func(s string) bool { return s == "registry.example.com/neutree/neutree-serve:v1.0.0" }), mock.Anything).
					Return(map[string]string{v1.ImageLabelVersion: "v1.0.0", v1.ImageLabelAcceleratorType: "nvidia_gpu"}, nil)
				imgSvc.On("GetImageLabels", mock.MatchedBy(func(s string) bool { return s == "registry.example.com/neutree/neutree-serve:v1.0.0-rocm" }), mock.Anything).
					Return(map[string]string{v1.ImageLabelVersion: "v1.0.0", v1.ImageLabelAcceleratorType: "amd_gpu"}, nil)
			},
			expectedStatusCode: http.StatusOK,
			expectedResponse: &availableUpgradeVersionsResponse{
				CurrentVersion:    "v1.0.0",
				AvailableVersions: []string{"v1.0.0"},
			},
		},
		{
			name:        "success - amd_gpu cluster only sees rocm versions",
			workspace:   "default",
			clusterName: "test-cluster",
			setupMock: func(s *storageMocks.MockStorage, imgSvc *registryMocks.MockImageService) {
				amdGPU := string(v1.AcceleratorTypeAMDGPU)
				s.On("ListCluster", mock.Anything).Return([]v1.Cluster{
					{
						Spec: &v1.ClusterSpec{
							Type:          v1.SSHClusterType,
							Version:       "v1.0.0",
							ImageRegistry: "my-registry",
						},
						Status: &v1.ClusterStatus{
							AcceleratorType: &amdGPU,
						},
					},
				}, nil)
				s.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{
					{
						Spec: &v1.ImageRegistrySpec{
							URL: "registry.example.com",
						},
					},
				}, nil)
				imgSvc.On("ListImageTags", mock.Anything, mock.Anything).Return([]string{
					"v1.0.0", "v1.0.0-rocm", "v1.1.0", "v1.1.0-rocm",
				}, nil)
				imgSvc.On("GetImageLabels", mock.MatchedBy(func(s string) bool { return s == "registry.example.com/neutree/neutree-serve:v1.0.0" }), mock.Anything).
					Return(map[string]string{v1.ImageLabelVersion: "v1.0.0", v1.ImageLabelAcceleratorType: "nvidia_gpu"}, nil)
				imgSvc.On("GetImageLabels", mock.MatchedBy(func(s string) bool { return s == "registry.example.com/neutree/neutree-serve:v1.0.0-rocm" }), mock.Anything).
					Return(map[string]string{v1.ImageLabelVersion: "v1.0.0", v1.ImageLabelAcceleratorType: "amd_gpu"}, nil)
				imgSvc.On("GetImageLabels", mock.MatchedBy(func(s string) bool { return s == "registry.example.com/neutree/neutree-serve:v1.1.0" }), mock.Anything).
					Return(map[string]string{v1.ImageLabelVersion: "v1.1.0", v1.ImageLabelAcceleratorType: "nvidia_gpu"}, nil)
				imgSvc.On("GetImageLabels", mock.MatchedBy(func(s string) bool { return s == "registry.example.com/neutree/neutree-serve:v1.1.0-rocm" }), mock.Anything).
					Return(map[string]string{v1.ImageLabelVersion: "v1.1.0", v1.ImageLabelAcceleratorType: "amd_gpu"}, nil)
			},
			expectedStatusCode: http.StatusOK,
			expectedResponse: &availableUpgradeVersionsResponse{
				CurrentVersion:    "v1.0.0",
				AvailableVersions: []string{"v1.0.0", "v1.1.0"},
			},
		},
		{
			name:        "success - k8s cluster uses router image with nvidia default",
			workspace:   "default",
			clusterName: "test-cluster",
			setupMock: func(s *storageMocks.MockStorage, imgSvc *registryMocks.MockImageService) {
				s.On("ListCluster", mock.Anything).Return([]v1.Cluster{
					{
						Spec: &v1.ClusterSpec{
							Type:          v1.KubernetesClusterType,
							Version:       "v1.0.0",
							ImageRegistry: "my-registry",
						},
					},
				}, nil)
				s.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{
					{
						Spec: &v1.ImageRegistrySpec{
							URL: "registry.example.com",
						},
					},
				}, nil)
				imgSvc.On("ListImageTags", "registry.example.com/"+v1.NeutreeRouterImageName, mock.Anything).Return([]string{
					"v1.0.0", "v1.1.0",
				}, nil)
				imgSvc.On("GetImageLabels", mock.MatchedBy(func(s string) bool { return s == "registry.example.com/neutree/router:v1.0.0" }), mock.Anything).
					Return(map[string]string{v1.ImageLabelVersion: "v1.0.0", v1.ImageLabelAcceleratorType: "nvidia_gpu"}, nil)
				imgSvc.On("GetImageLabels", mock.MatchedBy(func(s string) bool { return s == "registry.example.com/neutree/router:v1.1.0" }), mock.Anything).
					Return(map[string]string{v1.ImageLabelVersion: "v1.1.0", v1.ImageLabelAcceleratorType: "nvidia_gpu"}, nil)
			},
			expectedStatusCode: http.StatusOK,
			expectedResponse: &availableUpgradeVersionsResponse{
				CurrentVersion:    "v1.0.0",
				AvailableVersions: []string{"v1.0.0", "v1.1.0"},
			},
		},
		{
			name:        "cluster not found",
			workspace:   "default",
			clusterName: "nonexistent",
			setupMock: func(s *storageMocks.MockStorage, imgSvc *registryMocks.MockImageService) {
				s.On("ListCluster", mock.Anything).Return([]v1.Cluster{}, nil)
			},
			expectedStatusCode: http.StatusNotFound,
			expectedError:      "cluster default/nonexistent not found",
		},
		{
			name:        "cluster list error",
			workspace:   "default",
			clusterName: "test-cluster",
			setupMock: func(s *storageMocks.MockStorage, imgSvc *registryMocks.MockImageService) {
				s.On("ListCluster", mock.Anything).Return(nil, errors.New("db error"))
			},
			expectedStatusCode: http.StatusInternalServerError,
			expectedError:      "failed to get cluster",
		},
		{
			name:        "image registry not found",
			workspace:   "default",
			clusterName: "test-cluster",
			setupMock: func(s *storageMocks.MockStorage, imgSvc *registryMocks.MockImageService) {
				s.On("ListCluster", mock.Anything).Return([]v1.Cluster{
					{
						Spec: &v1.ClusterSpec{
							Type:          v1.SSHClusterType,
							Version:       "v1.0.0",
							ImageRegistry: "missing-registry",
						},
					},
				}, nil)
				s.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{}, nil)
			},
			expectedStatusCode: http.StatusNotFound,
			expectedError:      "image registry missing-registry not found",
		},
		{
			name:        "list tags error",
			workspace:   "default",
			clusterName: "test-cluster",
			setupMock: func(s *storageMocks.MockStorage, imgSvc *registryMocks.MockImageService) {
				s.On("ListCluster", mock.Anything).Return([]v1.Cluster{
					{
						Spec: &v1.ClusterSpec{
							Type:          v1.SSHClusterType,
							Version:       "v1.0.0",
							ImageRegistry: "my-registry",
						},
					},
				}, nil)
				s.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{
					{
						Spec: &v1.ImageRegistrySpec{
							URL: "registry.example.com",
						},
					},
				}, nil)
				imgSvc.On("ListImageTags", mock.Anything, mock.Anything).Return(nil, errors.New("registry unreachable"))
			},
			expectedStatusCode: http.StatusInternalServerError,
			expectedError:      "failed to list image tags",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := storageMocks.NewMockStorage(t)
			mockImgSvc := registryMocks.NewMockImageService(t)
			tt.setupMock(mockStorage, mockImgSvc)

			// Override newImageService for test
			origFactory := newImageService
			newImageService = func() registry.ImageService {
				return mockImgSvc
			}
			defer func() { newImageService = origFactory }()

			deps := &Dependencies{
				Storage: mockStorage,
			}

			c, w := createTestContext(gin.Params{
				{Key: "workspace", Value: tt.workspace},
				{Key: "name", Value: tt.clusterName},
			})

			handler := getAvailableUpgradeVersions(deps)
			handler(c)

			assert.Equal(t, tt.expectedStatusCode, w.Code)

			if tt.expectedResponse != nil {
				var resp availableUpgradeVersionsResponse
				err := json.Unmarshal(w.Body.Bytes(), &resp)
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedResponse.CurrentVersion, resp.CurrentVersion)
				assert.Equal(t, tt.expectedResponse.AvailableVersions, resp.AvailableVersions)
			}

			if tt.expectedError != "" {
				var errResp map[string]string
				err := json.Unmarshal(w.Body.Bytes(), &errResp)
				assert.NoError(t, err)
				assert.Contains(t, errResp["error"], tt.expectedError)
			}

			mockStorage.AssertExpectations(t)
			mockImgSvc.AssertExpectations(t)
		})
	}
}

