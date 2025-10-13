package monitoring

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	"github.com/neutree-ai/neutree/internal/ray/dashboard/mocks"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
)

func TestClusterMonitor_GetMetricsScrapeTargetsConfig(t *testing.T) {
	tests := []struct {
		name          string
		cluster       *v1.Cluster
		mockNodes     []v1.NodeSummary
		mockError     error
		expected      []v1.MetricsScrapeTargetsConfig
		expectedError string
	}{
		{
			name: "success with head and worker nodes",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{Name: "test-cluster"},
				Status: &v1.ClusterStatus{
					DashboardURL: "http://example-dashboard-url",
				},
			},
			mockNodes: []v1.NodeSummary{
				{
					IP: "10.0.0.1",
					Raylet: v1.Raylet{
						IsHeadNode: true,
						State:      v1.AliveNodeState,
					},
				},
				{
					IP: "10.0.0.2",
					Raylet: v1.Raylet{
						IsHeadNode: false,
						State:      v1.AliveNodeState,
					},
				},
			},
			expected: []v1.MetricsScrapeTargetsConfig{
				{
					Labels: map[string]string{
						"ray_io_cluster": "test-cluster",
						"job":            "ray",
					},
					Targets: []string{
						"10.0.0.1:44227", // DashboardMetricsPort
						"10.0.0.1:44217", // AutoScaleMetricsPort
						"10.0.0.1:54311", // RayletMetricsPort
						"10.0.0.2:54311", // Worker node RayletMetricsPort
					},
				},
			},
		},
		{
			name: "success with only head node",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{Name: "test-cluster"},
				Status: &v1.ClusterStatus{
					DashboardURL: "http://example-dashboard-url",
				},
			},
			mockNodes: []v1.NodeSummary{
				{
					IP: "10.0.0.1",
					Raylet: v1.Raylet{
						IsHeadNode: true,
						State:      v1.AliveNodeState,
					},
				},
			},
			expected: []v1.MetricsScrapeTargetsConfig{
				{
					Labels: map[string]string{
						"ray_io_cluster": "test-cluster",
						"job":            "ray",
					},
					Targets: []string{
						"10.0.0.1:44227",
						"10.0.0.1:44217",
						"10.0.0.1:54311",
					},
				},
			},
		},
		{
			name: "error listing nodes",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{Name: "test-cluster"},
				Status: &v1.ClusterStatus{
					DashboardURL: "http://example-dashboard-url",
				},
			},
			mockError:     errors.New("connection error"),
			expectedError: "failed to list ray nodes",
		},
		{
			name: "skip non-alive worker nodes",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{Name: "test-cluster"},
				Status: &v1.ClusterStatus{
					DashboardURL: "http://example-dashboard-url",
				},
			},
			mockNodes: []v1.NodeSummary{
				{
					IP: "10.0.0.1",
					Raylet: v1.Raylet{
						IsHeadNode: true,
						State:      v1.AliveNodeState,
					},
				},
				{
					IP: "10.0.0.2",
					Raylet: v1.Raylet{
						IsHeadNode: false,
						State:      v1.DeadNodeState,
					},
				},
			},
			expected: []v1.MetricsScrapeTargetsConfig{
				{
					Labels: map[string]string{
						"ray_io_cluster": "test-cluster",
						"job":            "ray",
					},
					Targets: []string{
						"10.0.0.1:44227",
						"10.0.0.1:44217",
						"10.0.0.1:54311",
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup mock orchestrator
			mockDashboardSvc := &mocks.MockDashboardService{}
			mockDashboardSvc.On("ListNodes").Return(tt.mockNodes, tt.mockError)
			dashboard.NewDashboardService = func(dashboardURL string) dashboard.DashboardService {
				assert.Equal(t, tt.cluster.Status.DashboardURL, dashboardURL)
				return mockDashboardSvc
			}

			// Create cluster monitor
			monitor := NewClusterMonitor(tt.cluster)

			// Execute test
			result, err := monitor.GetMetricsScrapeTargetsConfig()

			// Verify results
			if tt.expectedError != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}
