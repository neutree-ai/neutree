package cluster

import (
	"net/url"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	dashboardmocks "github.com/neutree-ai/neutree/internal/orchestrator/ray/dashboard/mocks"
	registrymocks "github.com/neutree-ai/neutree/internal/registry/mocks"
)

func TestGetClusterImage(t *testing.T) {
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
			got, err := getClusterImage(tt.cluster, tt.imageRegistry)

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

func TestCheckClusterImage(t *testing.T) {
	// Setup test cases with table-driven style
	tests := []struct {
		name          string
		setupMocks    func(*registrymocks.MockImageService)
		cluster       *v1.Cluster
		imageRegistry *v1.ImageRegistry
		expectedErr   error
		wantErr       bool
	}{
		{
			name: "image registry not connected",
			imageRegistry: &v1.ImageRegistry{
				Metadata: &v1.Metadata{Name: "test-registry"},
				Status:   &v1.ImageRegistryStatus{Phase: v1.ImageRegistryPhaseFAILED},
			},
			cluster:     &v1.Cluster{Metadata: &v1.Metadata{Name: "test-cluster"}},
			expectedErr: errors.New("image registry test-registry not connected"),
			wantErr:     true,
		},
		{
			name: "image not found",
			setupMocks: func(m *registrymocks.MockImageService) {
				m.On("CheckImageExists", mock.Anything, mock.Anything).Return(false, nil)
			},
			imageRegistry: &v1.ImageRegistry{
				Metadata: &v1.Metadata{Name: "test-registry"},
				Status:   &v1.ImageRegistryStatus{Phase: v1.ImageRegistryPhaseCONNECTED},
				Spec: &v1.ImageRegistrySpec{
					URL:        "https://registry.example.com",
					Repository: "repo",
					AuthConfig: v1.ImageRegistryAuthConfig{
						Username: "user",
						Password: "pass",
					},
				},
			},
			cluster:     &v1.Cluster{Spec: &v1.ClusterSpec{Version: "v1.0.0"}},
			expectedErr: errors.Wrap(ErrImageNotFound, "image registry.example.com/repo/neutree-serve:v1.0.0 not found"),
			wantErr:     true,
		},
		{
			name: "image check error",
			setupMocks: func(m *registrymocks.MockImageService) {
				m.On("CheckImageExists", mock.Anything, mock.Anything).Return(false, errors.New("connection error"))
			},
			imageRegistry: &v1.ImageRegistry{
				Metadata: &v1.Metadata{Name: "test-registry"},
				Status:   &v1.ImageRegistryStatus{Phase: v1.ImageRegistryPhaseCONNECTED},
				Spec: &v1.ImageRegistrySpec{
					URL:        "https://registry.example.com",
					Repository: "repo",
					AuthConfig: v1.ImageRegistryAuthConfig{
						Username: "user",
						Password: "pass",
					},
				},
			},
			cluster: &v1.Cluster{Spec: &v1.ClusterSpec{Version: "v1.0.0"}},
			wantErr: true,
		},
		{
			name: "success case",
			setupMocks: func(m *registrymocks.MockImageService) {
				m.On("CheckImageExists", mock.Anything, mock.Anything).Return(true, nil)
			},
			imageRegistry: &v1.ImageRegistry{
				Metadata: &v1.Metadata{Name: "test-registry"},
				Status:   &v1.ImageRegistryStatus{Phase: v1.ImageRegistryPhaseCONNECTED},
				Spec: &v1.ImageRegistrySpec{
					URL:        "https://registry.example.com",
					Repository: "repo",
					AuthConfig: v1.ImageRegistryAuthConfig{
						Username: "user",
						Password: "pass",
					},
				},
			},
			cluster: &v1.Cluster{
				Spec: &v1.ClusterSpec{Version: "v1.0.0"},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup mocks
			mockImageService := registrymocks.NewMockImageService(t)
			if tt.setupMocks != nil {
				tt.setupMocks(mockImageService)
			}

			// Execute test
			err := checkClusterImage(mockImageService, tt.cluster, tt.imageRegistry)

			// Verify results
			if tt.wantErr {
				assert.Error(t, err)
				if tt.expectedErr != nil {
					assert.Contains(t, err.Error(), tt.expectedErr.Error())
				}
			} else {
				assert.NoError(t, err)
			}
			mockImageService.AssertExpectations(t)
		})
	}
}

func TestGenerateRayClusterMetricsScrapeTargetsConfig(t *testing.T) {
	// Setup test cases with table-driven style
	tests := []struct {
		name        string
		nodes       []v1.NodeSummary
		mockSetup   func(*dashboardmocks.MockDashboardService)
		wantTargets []string
		wantErr     bool
	}{
		{
			name: "single head node",
			nodes: []v1.NodeSummary{
				{
					IP:     "192.168.1.1",
					Raylet: v1.Raylet{IsHeadNode: true, State: v1.AliveNodeState},
				},
			},
			mockSetup: func(m *dashboardmocks.MockDashboardService) {
				m.On("ListNodes").Return([]v1.NodeSummary{
					{
						IP:     "192.168.1.1",
						Raylet: v1.Raylet{IsHeadNode: true, State: v1.AliveNodeState},
					},
				}, nil)
			},
			wantTargets: []string{
				"192.168.1.1:44227",
				"192.168.1.1:44217",
				"192.168.1.1:54311",
			},
			wantErr: false,
		},
		{
			name: "head node with alive worker",
			nodes: []v1.NodeSummary{
				{
					IP:     "192.168.1.1",
					Raylet: v1.Raylet{IsHeadNode: true, State: v1.AliveNodeState},
				},
				{
					IP:     "192.168.1.2",
					Raylet: v1.Raylet{IsHeadNode: false, State: v1.AliveNodeState},
				},
			},
			mockSetup: func(m *dashboardmocks.MockDashboardService) {
				m.On("ListNodes").Return([]v1.NodeSummary{
					{
						IP:     "192.168.1.1",
						Raylet: v1.Raylet{IsHeadNode: true, State: v1.AliveNodeState},
					},
					{
						IP:     "192.168.1.2",
						Raylet: v1.Raylet{IsHeadNode: false, State: v1.AliveNodeState},
					},
				}, nil)
			},
			wantTargets: []string{
				"192.168.1.1:44227",
				"192.168.1.1:44217",
				"192.168.1.1:54311",
				"192.168.1.2:54311",
			},
			wantErr: false,
		},
		{
			name: "node list error",
			mockSetup: func(m *dashboardmocks.MockDashboardService) {
				m.On("ListNodes").Return(nil, errors.New("connection failed"))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup mocks
			mockDashboard := dashboardmocks.NewMockDashboardService(t)
			if tt.mockSetup != nil {
				tt.mockSetup(mockDashboard)
			}

			cluster := &v1.Cluster{
				Metadata: &v1.Metadata{Name: "test-cluster"},
			}

			// Execute function
			config, err := generateRayClusterMetricsScrapeTargetsConfig(cluster, mockDashboard)

			// Verify results
			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, "test-cluster", config.Labels["ray_io_cluster"])
			assert.Equal(t, "ray", config.Labels["job"])
			assert.ElementsMatch(t, tt.wantTargets, config.Targets)
			mockDashboard.AssertExpectations(t)
		})
	}
}
