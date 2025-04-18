package monitoring

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/orchestrator/mocks"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
)

func TestClusterMonitor_GetMetricsScrapeTargetsConfig(t *testing.T) {
	tests := []struct {
		name          string
		cluster       *v1.Cluster
		mockNodes     []v1.NodeSummary
		mockError     error
		expected      []MetricsScrapeTargetsConfig
		expectedError string
	}{
		{
			name: "success with head and worker nodes",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{Name: "test-cluster"},
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
			expected: []MetricsScrapeTargetsConfig{
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
			expected: []MetricsScrapeTargetsConfig{
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
			},
			mockError:     errors.New("connection error"),
			expectedError: "failed to list ray nodes",
		},
		{
			name: "skip non-alive worker nodes",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{Name: "test-cluster"},
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
			expected: []MetricsScrapeTargetsConfig{
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
			mockOrchestrator := &mocks.MockOrchestrator{}
			mockOrchestrator.On("ListNodes").Return(tt.mockNodes, tt.mockError)

			// Create cluster monitor
			monitor := NewClusterMonitor(tt.cluster, mockOrchestrator)

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
