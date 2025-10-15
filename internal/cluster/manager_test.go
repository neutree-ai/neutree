package cluster

import (
	"net/url"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	registrymocks "github.com/neutree-ai/neutree/internal/registry/mocks"
)

func TestGetBaseImage(t *testing.T) {
	tests := []struct {
		name          string
		cluster       *v1.Cluster
		imageRegistry *v1.ImageRegistry
		want          string
		wantErr       bool
	}{
		{
			name: "valid URL with standard repository",
			cluster: &v1.Cluster{
				Spec: &v1.ClusterSpec{
					Version: "v1.2.3",
				},
			},
			imageRegistry: &v1.ImageRegistry{
				Spec: &v1.ImageRegistrySpec{
					URL:        "https://registry.example.com",
					Repository: "my-repo",
				},
			},
			want:    "registry.example.com/my-repo/neutree-serve:v1.2.3",
			wantErr: false,
		},
		{
			name: "invalid URL format",
			cluster: &v1.Cluster{
				Spec: &v1.ClusterSpec{
					Version: "v1.0.0",
				},
			},
			imageRegistry: &v1.ImageRegistry{
				Spec: &v1.ImageRegistrySpec{
					URL:        "::invalid-url::",
					Repository: "repo",
				},
			},
			want:    "",
			wantErr: true,
		},
		{
			name: "URL with port number",
			cluster: &v1.Cluster{
				Spec: &v1.ClusterSpec{
					Version: "v2.3.4",
				},
			},
			imageRegistry: &v1.ImageRegistry{
				Spec: &v1.ImageRegistrySpec{
					URL:        "https://registry.example.com:5000",
					Repository: "prod",
				},
			},
			want:    "registry.example.com:5000/prod/neutree-serve:v2.3.4",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := getBaseImage(tt.cluster, tt.imageRegistry)

			if tt.wantErr {
				assert.Error(t, err)
				// Verify error contains parsing error message
				if _, ok := err.(*url.Error); !ok {
					assert.Contains(t, err.Error(), "failed to parse image registry url")
				}
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestValidateImageRegistry(t *testing.T) {
	tests := []struct {
		name          string
		imageRegistry *v1.ImageRegistry
		wantErr       bool
	}{
		{
			name: "image registry URL is empty",
			imageRegistry: &v1.ImageRegistry{
				Metadata: &v1.Metadata{Name: "test-registry"},
				Spec: &v1.ImageRegistrySpec{
					URL:        "",
					Repository: "repo",
				},
			},
			wantErr: true,
		}, {
			name: "image registry repository is empty",
			imageRegistry: &v1.ImageRegistry{
				Metadata: &v1.Metadata{Name: "test-registry"},
				Spec: &v1.ImageRegistrySpec{
					URL:        "registry.example.com",
					Repository: "",
				},
			},
			wantErr: true,
		},
		{
			name: "image registry not connected",
			imageRegistry: &v1.ImageRegistry{
				Metadata: &v1.Metadata{Name: "test-registry"},
				Spec: &v1.ImageRegistrySpec{
					URL:        "registry.example.com",
					Repository: "test",
				},
				Status: &v1.ImageRegistryStatus{
					Phase: v1.ImageRegistryPhaseFAILED,
				},
			},
			wantErr: true,
		},
		{
			name: "image registry status is nil",
			imageRegistry: &v1.ImageRegistry{
				Metadata: &v1.Metadata{Name: "test-registry"},
				Spec: &v1.ImageRegistrySpec{
					URL:        "registry.example.com",
					Repository: "test",
				},
			},
			wantErr: true,
		},
		{
			name: "image registry connected",
			imageRegistry: &v1.ImageRegistry{
				Metadata: &v1.Metadata{Name: "test-registry"},
				Spec: &v1.ImageRegistrySpec{
					URL:        "registry.example.com",
					Repository: "test",
				},
				Status: &v1.ImageRegistryStatus{
					Phase: v1.ImageRegistryPhaseCONNECTED,
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateImageRegistryFunc(tt.imageRegistry)()
			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "image registry")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateClusterImage(t *testing.T) {
	tests := []struct {
		name      string
		mockSetup func(*registrymocks.MockImageService)
		wantErr   bool
	}{
		{
			name: "image exists",
			mockSetup: func(m *registrymocks.MockImageService) {
				m.On("CheckImageExists", "test-image", mock.Anything).Return(true, nil)
			},
			wantErr: false,
		},
		{
			name: "image not exists",
			mockSetup: func(m *registrymocks.MockImageService) {
				m.On("CheckImageExists", "test-image", mock.Anything).Return(false, nil)
			},
			wantErr: true,
		},
		{
			name: "image service error",
			mockSetup: func(m *registrymocks.MockImageService) {
				m.On("CheckImageExists", "test-image", mock.Anything).Return(false, errors.New("connection failed"))
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockImageService := registrymocks.NewMockImageService(t)
			tt.mockSetup(mockImageService)
			err := validateClusterImageFunc(mockImageService, v1.ImageRegistryAuthConfig{}, "test-image")()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// func TestGenerateRayClusterMetricsScrapeTargetsConfig(t *testing.T) {
// 	// Setup test cases with table-driven style
// 	tests := []struct {
// 		name        string
// 		nodes       []v1.NodeSummary
// 		mockSetup   func(*dashboardmocks.MockDashboardService)
// 		wantTargets []string
// 		wantErr     bool
// 	}{
// 		{
// 			name: "single head node",
// 			nodes: []v1.NodeSummary{
// 				{
// 					IP:     "192.168.1.1",
// 					Raylet: v1.Raylet{IsHeadNode: true, State: v1.AliveNodeState},
// 				},
// 			},
// 			mockSetup: func(m *dashboardmocks.MockDashboardService) {
// 				m.On("ListNodes").Return([]v1.NodeSummary{
// 					{
// 						IP:     "192.168.1.1",
// 						Raylet: v1.Raylet{IsHeadNode: true, State: v1.AliveNodeState},
// 					},
// 				}, nil)
// 			},
// 			wantTargets: []string{
// 				"192.168.1.1:44227",
// 				"192.168.1.1:44217",
// 				"192.168.1.1:54311",
// 			},
// 			wantErr: false,
// 		},
// 		{
// 			name: "head node with alive worker",
// 			nodes: []v1.NodeSummary{
// 				{
// 					IP:     "192.168.1.1",
// 					Raylet: v1.Raylet{IsHeadNode: true, State: v1.AliveNodeState},
// 				},
// 				{
// 					IP:     "192.168.1.2",
// 					Raylet: v1.Raylet{IsHeadNode: false, State: v1.AliveNodeState},
// 				},
// 			},
// 			mockSetup: func(m *dashboardmocks.MockDashboardService) {
// 				m.On("ListNodes").Return([]v1.NodeSummary{
// 					{
// 						IP:     "192.168.1.1",
// 						Raylet: v1.Raylet{IsHeadNode: true, State: v1.AliveNodeState},
// 					},
// 					{
// 						IP:     "192.168.1.2",
// 						Raylet: v1.Raylet{IsHeadNode: false, State: v1.AliveNodeState},
// 					},
// 				}, nil)
// 			},
// 			wantTargets: []string{
// 				"192.168.1.1:44227",
// 				"192.168.1.1:44217",
// 				"192.168.1.1:54311",
// 				"192.168.1.2:54311",
// 			},
// 			wantErr: false,
// 		},
// 		{
// 			name: "node list error",
// 			mockSetup: func(m *dashboardmocks.MockDashboardService) {
// 				m.On("ListNodes").Return(nil, errors.New("connection failed"))
// 			},
// 			wantErr: true,
// 		},
// 	}

// 	for _, tt := range tests {
// 		t.Run(tt.name, func(t *testing.T) {
// 			// Setup mocks
// 			mockDashboard := dashboardmocks.NewMockDashboardService(t)
// 			if tt.mockSetup != nil {
// 				tt.mockSetup(mockDashboard)
// 			}

// 			cluster := &v1.Cluster{
// 				Metadata: &v1.Metadata{Name: "test-cluster"},
// 			}

// 			// Execute function
// 			config, err := generateRayClusterMetricsScrapeTargetsConfig(cluster, mockDashboard)

// 			// Verify results
// 			if tt.wantErr {
// 				assert.Error(t, err)
// 				return
// 			}

// 			assert.NoError(t, err)
// 			assert.Equal(t, "test-cluster", config.Labels["ray_io_cluster"])
// 			assert.Equal(t, "ray", config.Labels["job"])
// 			assert.ElementsMatch(t, tt.wantTargets, config.Targets)
// 			mockDashboard.AssertExpectations(t)
// 		})
// 	}
// }
