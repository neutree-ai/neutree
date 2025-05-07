package orchestrator

import (
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	v1 "github.com/neutree-ai/neutree/api/v1"
	clustermocks "github.com/neutree-ai/neutree/internal/orchestrator/ray/cluster/mocks"
	"github.com/neutree-ai/neutree/internal/orchestrator/ray/dashboard"
	dashboardmocks "github.com/neutree-ai/neutree/internal/orchestrator/ray/dashboard/mocks"
)

func TestClusterStatus(t *testing.T) {
	tests := []struct {
		name           string
		setupMock      func(*dashboardmocks.MockDashboardService)
		expectedStatus *v1.RayClusterStatus
		expectError    bool
	}{
		{
			name: "success - basic cluster status",
			setupMock: func(mock *dashboardmocks.MockDashboardService) {
				mock.On("ListNodes").Return([]v1.NodeSummary{
					{
						IP: "192.168.1.1",
						Raylet: v1.Raylet{
							IsHeadNode: false,
							State:      v1.AliveNodeState,
							Labels: map[string]string{
								v1.NeutreeServingVersionLabel: "v1.0.0",
							},
						},
					},
				}, nil)

				// Mock GetClusterAutoScaleStatus
				mock.On("GetClusterAutoScaleStatus").Return(v1.AutoscalerReport{
					ActiveNodes:     map[string]int{"worker": 1},
					PendingLaunches: map[string]int{},
					PendingNodes:    []v1.NodeInfo{},
					FailedNodes:     []v1.NodeInfo{},
				}, nil)

				// Mock GetClusterMetadata
				mock.On("GetClusterMetadata").Return(&dashboard.ClusterMetadataResponse{
					Data: v1.RayClusterMetadataData{
						PythonVersion: "3.8.10",
						RayVersion:    "2.0.0",
					},
				}, nil)
			},
			expectedStatus: &v1.RayClusterStatus{
				ReadyNodes:          1,
				DesireNodes:         1,
				NeutreeServeVersion: "v1.0.0",
				AutoScaleStatus: v1.AutoScaleStatus{
					PendingNodes: 0,
					ActiveNodes:  1,
					FailedNodes:  0,
				},
				PythonVersion: "3.8.10",
				RayVersion:    "2.0.0",
			},
			expectError: false,
		},
		{
			name: "success - skip head node",
			setupMock: func(mock *dashboardmocks.MockDashboardService) {
				mock.On("ListNodes").Return([]v1.NodeSummary{
					{
						IP: "192.168.1.1",
						Raylet: v1.Raylet{
							IsHeadNode: true,
							State:      v1.AliveNodeState,
							Labels: map[string]string{
								v1.NeutreeServingVersionLabel: "v1.0.0",
							},
						},
					},
				}, nil)

				// Mock GetClusterAutoScaleStatus
				mock.On("GetClusterAutoScaleStatus").Return(v1.AutoscalerReport{
					ActiveNodes:     map[string]int{"headgroup": 1},
					PendingLaunches: map[string]int{},
					PendingNodes:    []v1.NodeInfo{},
					FailedNodes:     []v1.NodeInfo{},
				}, nil)

				// Mock GetClusterMetadata
				mock.On("GetClusterMetadata").Return(&dashboard.ClusterMetadataResponse{
					Data: v1.RayClusterMetadataData{
						PythonVersion: "3.8.10",
						RayVersion:    "2.0.0",
					},
				}, nil)
			},
			expectedStatus: &v1.RayClusterStatus{
				ReadyNodes:          0,
				DesireNodes:         0,
				NeutreeServeVersion: "v1.0.0",
				AutoScaleStatus: v1.AutoScaleStatus{
					PendingNodes: 0,
					ActiveNodes:  0,
					FailedNodes:  0,
				},
				PythonVersion: "3.8.10",
				RayVersion:    "2.0.0",
			},
			expectError: false,
		},
		{
			name: "success - multiple versions",
			setupMock: func(mock *dashboardmocks.MockDashboardService) {
				mock.On("ListNodes").Return([]v1.NodeSummary{
					{
						IP: "192.168.1.1",
						Raylet: v1.Raylet{
							IsHeadNode: false,
							State:      v1.AliveNodeState,
							Labels: map[string]string{
								v1.NeutreeServingVersionLabel: "v1.0.0",
							},
						},
					},
					{
						IP: "192.168.1.2",
						Raylet: v1.Raylet{
							IsHeadNode: false,
							State:      v1.AliveNodeState,
							Labels: map[string]string{
								v1.NeutreeServingVersionLabel: "v1.1.0",
							},
						},
					},
				}, nil)
				mock.On("GetClusterAutoScaleStatus").Return(v1.AutoscalerReport{
					ActiveNodes:     map[string]int{"worker": 2},
					PendingLaunches: map[string]int{},
					PendingNodes:    []v1.NodeInfo{},
					FailedNodes:     []v1.NodeInfo{},
				}, nil)
				mock.On("GetClusterMetadata").Return(&dashboard.ClusterMetadataResponse{
					Data: v1.RayClusterMetadataData{
						PythonVersion: "3.8.10",
						RayVersion:    "2.0.0",
					},
				}, nil)
			},
			expectedStatus: &v1.RayClusterStatus{
				ReadyNodes:          2,
				DesireNodes:         2,
				NeutreeServeVersion: "v1.1.0",
				AutoScaleStatus: v1.AutoScaleStatus{
					PendingNodes: 0,
					ActiveNodes:  2,
					FailedNodes:  0,
				},
				PythonVersion: "3.8.10",
				RayVersion:    "2.0.0",
			},
			expectError: false,
		},
		{
			name: "error - list nodes failed",
			setupMock: func(mock *dashboardmocks.MockDashboardService) {
				mock.On("ListNodes").Return(nil, errors.New("connection error"))
			},
			expectError: true,
		},
		{
			name: "error - get autoscale status failed",
			setupMock: func(mock *dashboardmocks.MockDashboardService) {
				mock.On("ListNodes").Return([]v1.NodeSummary{}, nil)
				mock.On("GetClusterAutoScaleStatus").Return(v1.AutoscalerReport{}, errors.New("autoscale error"))
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockService := dashboardmocks.NewMockDashboardService(t)
			if tt.setupMock != nil {
				tt.setupMock(mockService)
			}

			mockClusterService := &clustermocks.MockClusterManager{}
			mockClusterService.On("GetDashboardService", mock.Anything).Return(mockService, nil)
			mockClusterService.On("GetDesireStaticWorkersIP", mock.Anything).Return([]string{})
			o := &RayOrchestrator{
				clusterHelper: mockClusterService,
				cluster: &v1.Cluster{
					Metadata: &v1.Metadata{Name: "test-cluster"},
					Spec: &v1.ClusterSpec{
						Version: "v1.0.0",
						Config:  map[string]interface{}{},
					},
					Status: &v1.ClusterStatus{
						Initialized: true,
					},
				},
			}

			status, err := o.ClusterStatus()

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, status)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedStatus, status)
			}

			mockService.AssertExpectations(t)
		})
	}
}
