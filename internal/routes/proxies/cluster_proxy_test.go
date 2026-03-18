package proxies

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Masterminds/semver/v3"
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
			name:        "success - deduplicates accelerator variants and sorts versions",
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
				imgSvc.On("ListImageTags", mock.Anything, mock.Anything).Return([]string{
					"v0.9.0", "v1.0.0", "v1.0.0-rocm", "v1.0.1-rc.1", "v1.0.1-rc.1-rocm", "v1.1.0", "v1.1.0-rocm", "v2.0.0", "latest", "invalid",
				}, nil)
			},
			expectedStatusCode: http.StatusOK,
			expectedResponse: &availableUpgradeVersionsResponse{
				CurrentVersion:    "v1.0.0",
				AvailableVersions: []string{"v0.9.0", "v1.0.0", "v1.0.1-rc.1", "v1.1.0", "v2.0.0"},
			},
		},
		{
			name:        "success - returns all versions even when current is latest",
			workspace:   "default",
			clusterName: "test-cluster",
			setupMock: func(s *storageMocks.MockStorage, imgSvc *registryMocks.MockImageService) {
				s.On("ListCluster", mock.Anything).Return([]v1.Cluster{
					{
						Spec: &v1.ClusterSpec{
							Type:          v1.SSHClusterType,
							Version:       "v2.0.0",
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
				imgSvc.On("ListImageTags", mock.Anything, mock.Anything).Return([]string{
					"v1.0.0", "v1.5.0", "v2.0.0",
				}, nil)
			},
			expectedStatusCode: http.StatusOK,
			expectedResponse: &availableUpgradeVersionsResponse{
				CurrentVersion:    "v2.0.0",
				AvailableVersions: []string{"v1.0.0", "v1.5.0", "v2.0.0"},
			},
		},
		{
			name:        "success - k8s cluster uses router image",
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

func TestStripAcceleratorSuffix(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		// No prerelease — unchanged
		{"1.0.0", "1.0.0"},
		// Purely alphabetic prerelease — accelerator suffix, stripped
		{"1.0.0-rocm", "1.0.0"},
		{"1.0.0-ROCm", "1.0.0"},
		// Semantic prerelease (contains non-alpha chars) — kept
		{"1.0.1-rc.1", "1.0.1-rc.1"},
		{"1.0.0-alpha.1", "1.0.0-alpha.1"},
		{"1.0.0-beta.2", "1.0.0-beta.2"},
		// Prerelease + accelerator suffix (last hyphen-segment is alphabetic) — suffix stripped
		{"1.0.1-rc.1-rocm", "1.0.1-rc.1"},
		{"1.0.0-alpha.1-rocm", "1.0.0-alpha.1"},
		// Numeric-only prerelease — kept (not alphabetic)
		{"1.0.0-1", "1.0.0-1"},
		// Mixed alphanumeric last segment — kept (not purely alphabetic)
		{"1.0.0-build123", "1.0.0-build123"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			v := semver.MustParse(tt.input)
			result := stripAcceleratorSuffix(v)
			assert.Equal(t, tt.expected, result.String())
		})
	}
}
