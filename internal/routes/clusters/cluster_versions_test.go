package clusters

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
	registryMocks "github.com/neutree-ai/neutree/internal/registry/mocks"
	storageMocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func createTestContextWithQuery(queryParams map[string]string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	q := req.URL.Query()
	for k, v := range queryParams {
		q.Set(k, v)
	}
	req.URL.RawQuery = q.Encode()
	c.Request = req

	return c, w
}

func TestGetAvailableClusterVersions(t *testing.T) {
	tests := []struct {
		name               string
		queryParams        map[string]string
		setupMock          func(s *storageMocks.MockStorage, imgSvc *registryMocks.MockImageService)
		expectedStatusCode int
		expectedResponse   *availableClusterVersionsResponse
		expectedError      string
	}{
		{
			name: "success - filters by nvidia accelerator type",
			queryParams: map[string]string{
				"workspace":        "default",
				"image_registry":   "my-registry",
				"cluster_type":     "ssh",
				"accelerator_type": "nvidia_gpu",
			},
			setupMock: func(s *storageMocks.MockStorage, imgSvc *registryMocks.MockImageService) {
				s.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{
					{Spec: &v1.ImageRegistrySpec{URL: "registry.example.com"}},
				}, nil)
				imgSvc.On("ListImageTags", mock.Anything, mock.Anything).Return([]string{
					"v1.0.0", "v1.0.0-rocm", "v1.0.1-rc.1", "v1.1.0",
					"v1.0.1-nightly-20260313", "latest",
				}, nil)
				imgSvc.On("GetImageLabels", mock.MatchedBy(func(s string) bool { return s == "registry.example.com/neutree/neutree-serve:v1.0.0" }), mock.Anything).
					Return(map[string]string{v1.ImageLabelVersion: "v1.0.0", v1.ImageLabelAcceleratorType: "nvidia_gpu"}, nil)
				imgSvc.On("GetImageLabels", mock.MatchedBy(func(s string) bool { return s == "registry.example.com/neutree/neutree-serve:v1.0.0-rocm" }), mock.Anything).
					Return(map[string]string{v1.ImageLabelVersion: "v1.0.0", v1.ImageLabelAcceleratorType: "amd_gpu"}, nil)
				imgSvc.On("GetImageLabels", mock.MatchedBy(func(s string) bool { return s == "registry.example.com/neutree/neutree-serve:v1.0.1-rc.1" }), mock.Anything).
					Return(map[string]string{v1.ImageLabelVersion: "v1.0.1-rc.1", v1.ImageLabelAcceleratorType: "nvidia_gpu"}, nil)
				imgSvc.On("GetImageLabels", mock.MatchedBy(func(s string) bool { return s == "registry.example.com/neutree/neutree-serve:v1.1.0" }), mock.Anything).
					Return(map[string]string{v1.ImageLabelVersion: "v1.1.0", v1.ImageLabelAcceleratorType: "nvidia_gpu"}, nil)
				// Unlabeled tags — skipped
				imgSvc.On("GetImageLabels", mock.MatchedBy(func(s string) bool { return s == "registry.example.com/neutree/neutree-serve:v1.0.1-nightly-20260313" }), mock.Anything).
					Return(map[string]string{}, nil)
				imgSvc.On("GetImageLabels", mock.MatchedBy(func(s string) bool { return s == "registry.example.com/neutree/neutree-serve:latest" }), mock.Anything).
					Return(map[string]string{}, nil)
			},
			expectedStatusCode: http.StatusOK,
			expectedResponse: &availableClusterVersionsResponse{
				AvailableVersions: []string{"v1.0.0", "v1.0.1-rc.1", "v1.1.0"},
			},
		},
		{
			name: "success - no accelerator_type deduplicates shared versions",
			queryParams: map[string]string{
				"workspace":      "default",
				"image_registry": "my-registry",
				"cluster_type":   "ssh",
			},
			setupMock: func(s *storageMocks.MockStorage, imgSvc *registryMocks.MockImageService) {
				s.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{
					{Spec: &v1.ImageRegistrySpec{URL: "registry.example.com"}},
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
			expectedResponse: &availableClusterVersionsResponse{
				AvailableVersions: []string{"v1.0.0"},
			},
		},
		{
			name: "success - k8s cluster type uses router image",
			queryParams: map[string]string{
				"workspace":        "default",
				"image_registry":   "my-registry",
				"cluster_type":     "kubernetes",
				"accelerator_type": "nvidia_gpu",
			},
			setupMock: func(s *storageMocks.MockStorage, imgSvc *registryMocks.MockImageService) {
				s.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{
					{Spec: &v1.ImageRegistrySpec{URL: "registry.example.com"}},
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
			expectedResponse: &availableClusterVersionsResponse{
				AvailableVersions: []string{"v1.0.0", "v1.1.0"},
			},
		},
		{
			name:        "missing workspace",
			queryParams: map[string]string{"image_registry": "r", "cluster_type": "ssh"},
			setupMock:   func(s *storageMocks.MockStorage, imgSvc *registryMocks.MockImageService) {},
			expectedStatusCode: http.StatusBadRequest,
			expectedError:      "workspace is required",
		},
		{
			name:        "missing image_registry",
			queryParams: map[string]string{"workspace": "default", "cluster_type": "ssh"},
			setupMock:   func(s *storageMocks.MockStorage, imgSvc *registryMocks.MockImageService) {},
			expectedStatusCode: http.StatusBadRequest,
			expectedError:      "image_registry is required",
		},
		{
			name:        "missing cluster_type",
			queryParams: map[string]string{"workspace": "default", "image_registry": "r"},
			setupMock:   func(s *storageMocks.MockStorage, imgSvc *registryMocks.MockImageService) {},
			expectedStatusCode: http.StatusBadRequest,
			expectedError:      "cluster_type is required",
		},
		{
			name: "image registry not found",
			queryParams: map[string]string{
				"workspace": "default", "image_registry": "missing", "cluster_type": "ssh",
			},
			setupMock: func(s *storageMocks.MockStorage, imgSvc *registryMocks.MockImageService) {
				s.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{}, nil)
			},
			expectedStatusCode: http.StatusNotFound,
			expectedError:      "image registry default/missing not found",
		},
		{
			name: "list tags error",
			queryParams: map[string]string{
				"workspace": "default", "image_registry": "my-registry", "cluster_type": "ssh",
			},
			setupMock: func(s *storageMocks.MockStorage, imgSvc *registryMocks.MockImageService) {
				s.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{
					{Spec: &v1.ImageRegistrySpec{URL: "registry.example.com"}},
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

			deps := &Dependencies{Storage: mockStorage, ImageService: mockImgSvc}
			c, w := createTestContextWithQuery(tt.queryParams)

			handler := getAvailableClusterVersions(deps)
			handler(c)

			assert.Equal(t, tt.expectedStatusCode, w.Code)

			if tt.expectedResponse != nil {
				var resp availableClusterVersionsResponse
				err := json.Unmarshal(w.Body.Bytes(), &resp)
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedResponse.AvailableVersions, resp.AvailableVersions)
			}

			if tt.expectedError != "" {
				var errResp map[string]string
				err := json.Unmarshal(w.Body.Bytes(), &errResp)
				assert.NoError(t, err)
				assert.Contains(t, errResp["error"], tt.expectedError)
			}
		})
	}
}
