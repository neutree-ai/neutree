package orchestrator

import (
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"go.openly.dev/pointy"

	v1 "github.com/neutree-ai/neutree/api/v1"
	clustermocks "github.com/neutree-ai/neutree/internal/orchestrator/ray/cluster/mocks"
	"github.com/neutree-ai/neutree/internal/orchestrator/ray/dashboard"
	dashboardmocks "github.com/neutree-ai/neutree/internal/orchestrator/ray/dashboard/mocks"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
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

func TestRayOrchestrator_ApplicationNamingConsistency(t *testing.T) {
	endpoint := &v1.Endpoint{
		Metadata: &v1.Metadata{
			Workspace: "production",
			Name:      "chat-model",
		},
		Spec: &v1.EndpointSpec{
			Cluster: "test-cluster",
			Engine: &v1.EndpointEngineSpec{
				Engine:  "vllm",
				Version: "0.5.0",
			},
			Model: &v1.ModelSpec{
				Registry: "test-registry",
				Name:     "test-model",
			},
			Resources: &v1.ResourceSpec{
				CPU:         pointy.Float64(1.0),
				GPU:         pointy.Float64(1.0),
				Accelerator: make(map[string]float64),
			},
			Replicas: v1.ReplicaSpec{
				Num: pointy.Int(1),
			},
			DeploymentOptions: map[string]interface{}{},
			Variables:         map[string]interface{}{},
		},
	}

	expectedAppName := dashboard.EndpointToServeApplicationName(endpoint)
	assert.Equal(t, "production_chat-model", expectedAppName)

	tests := []struct {
		name        string
		setupMock   func(*dashboardmocks.MockDashboardService, *storagemocks.MockStorage)
		testFunc    func(*RayOrchestrator) error
		expectError bool
	}{
		{
			name: "CreateEndpoint uses consistent app name",
			setupMock: func(mockDashboard *dashboardmocks.MockDashboardService, mockStorage *storagemocks.MockStorage) {
				// Mock storage dependencies
				mockStorage.On("ListCluster", mock.Anything).Return([]v1.Cluster{{
					Metadata: &v1.Metadata{Name: "test-cluster"},
				}}, nil)
				mockStorage.On("ListEngine", mock.Anything).Return([]v1.Engine{{
					Metadata: &v1.Metadata{Name: "vllm"},
					Spec: &v1.EngineSpec{
						Versions: []*v1.EngineVersion{{Version: "0.5.0"}},
					},
					Status: &v1.EngineStatus{Phase: v1.EnginePhaseCreated},
				}}, nil)
				mockStorage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{{
					Metadata: &v1.Metadata{Name: "test-registry"},
					Spec: &v1.ModelRegistrySpec{
						Type: v1.HuggingFaceModelRegistryType,
						Url:  "https://huggingface.co",
					},
					Status: &v1.ModelRegistryStatus{Phase: v1.ModelRegistryPhaseCONNECTED},
				}}, nil)

				// Mock dashboard service
				mockDashboard.On("GetServeApplications").Return(&dashboard.RayServeApplicationsResponse{
					Applications: map[string]dashboard.RayServeApplicationStatus{},
				}, nil)
				mockDashboard.On("UpdateServeApplications", mock.MatchedBy(func(req dashboard.RayServeApplicationsRequest) bool {
					// Verify that the application name is consistent
					for _, app := range req.Applications {
						if app.Name == expectedAppName {
							return true
						}
					}
					return false
				})).Return(nil)
			},
			testFunc: func(o *RayOrchestrator) error {
				_, err := o.CreateEndpoint(endpoint)
				return err
			},
			expectError: false,
		},
		{
			name: "DeleteEndpoint uses consistent app name",
			setupMock: func(mockDashboard *dashboardmocks.MockDashboardService, mockStorage *storagemocks.MockStorage) {
				// Mock storage dependencies
				mockStorage.On("ListCluster", mock.Anything).Return([]v1.Cluster{{
					Metadata: &v1.Metadata{Name: "test-cluster"},
				}}, nil)

				// Mock dashboard service with existing application
				mockDashboard.On("GetServeApplications").Return(&dashboard.RayServeApplicationsResponse{
					Applications: map[string]dashboard.RayServeApplicationStatus{
						expectedAppName: {
							Status: "RUNNING",
							DeployedAppConfig: &dashboard.RayServeApplication{
								Name: expectedAppName,
							},
						},
						"other_app": {
							Status: "RUNNING",
							DeployedAppConfig: &dashboard.RayServeApplication{
								Name: "other_app",
							},
						},
					},
				}, nil)
				// Verify that the deleted application is removed from the list
				mockDashboard.On("UpdateServeApplications", mock.MatchedBy(func(req dashboard.RayServeApplicationsRequest) bool {
					// Ensure the expected app name is not in the updated list
					for _, app := range req.Applications {
						if app.Name == expectedAppName {
							return false
						}
					}
					return true
				})).Return(nil)
			},
			testFunc: func(o *RayOrchestrator) error {
				return o.DeleteEndpoint(endpoint)
			},
			expectError: false,
		},
		{
			name: "GetEndpointStatus uses consistent app name",
			setupMock: func(mockDashboard *dashboardmocks.MockDashboardService, mockStorage *storagemocks.MockStorage) {
				mockDashboard.On("GetServeApplications").Return(&dashboard.RayServeApplicationsResponse{
					Applications: map[string]dashboard.RayServeApplicationStatus{
						expectedAppName: {
							Status:  "RUNNING",
							Message: "Application is healthy",
						},
					},
				}, nil)
			},
			testFunc: func(o *RayOrchestrator) error {
				status, err := o.GetEndpointStatus(endpoint)
				if err != nil {
					return err
				}
				assert.Equal(t, v1.EndpointPhaseRUNNING, status.Phase)
				return nil
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockDashboard := dashboardmocks.NewMockDashboardService(t)
			mockStorage := storagemocks.NewMockStorage(t)

			if tt.setupMock != nil {
				tt.setupMock(mockDashboard, mockStorage)
			}

			mockClusterService := &clustermocks.MockClusterManager{}
			mockClusterService.On("GetDashboardService", mock.Anything).Return(mockDashboard, nil)

			o := &RayOrchestrator{
				clusterHelper: mockClusterService,
				storage:       mockStorage,
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
				opTimeout: OperationConfig{
					CommonTimeout: 600000000000, // 10 minutes
				},
			}

			err := tt.testFunc(o)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			mockDashboard.AssertExpectations(t)
			mockStorage.AssertExpectations(t)
		})
	}
}

func TestRayOrchestrator_CreateEndpoint_ApplicationNameConsistency(t *testing.T) {
	endpoint := &v1.Endpoint{
		Metadata: &v1.Metadata{
			Workspace: "production",
			Name:      "chat-model",
		},
		Spec: &v1.EndpointSpec{
			Cluster: "test-cluster",
			Engine: &v1.EndpointEngineSpec{
				Engine:  "vllm",
				Version: "0.5.0",
			},
			Model: &v1.ModelSpec{
				Registry: "test-registry",
				Name:     "test-model",
			},
			Resources: &v1.ResourceSpec{
				CPU:         pointy.Float64(1.0),
				GPU:         pointy.Float64(1.0),
				Accelerator: make(map[string]float64),
			},
			Replicas: v1.ReplicaSpec{
				Num: pointy.Int(1),
			},
			DeploymentOptions: map[string]interface{}{},
			Variables:         map[string]interface{}{},
		},
	}

	expectedAppName := dashboard.EndpointToServeApplicationName(endpoint)
	assert.Equal(t, "production_chat-model", expectedAppName)

	tests := []struct {
		name          string
		setupMock     func(*dashboardmocks.MockDashboardService, *storagemocks.MockStorage)
		expectError   bool
		expectedPhase v1.EndpointPhase
	}{
		{
			name: "CreateEndpoint with new application",
			setupMock: func(mockDashboard *dashboardmocks.MockDashboardService, mockStorage *storagemocks.MockStorage) {
				// Mock storage dependencies
				mockStorage.On("ListCluster", mock.Anything).Return([]v1.Cluster{{
					Metadata: &v1.Metadata{Name: "test-cluster"},
				}}, nil)
				mockStorage.On("ListEngine", mock.Anything).Return([]v1.Engine{{
					Metadata: &v1.Metadata{Name: "vllm"},
					Spec: &v1.EngineSpec{
						Versions: []*v1.EngineVersion{{Version: "0.5.0"}},
					},
					Status: &v1.EngineStatus{Phase: v1.EnginePhaseCreated},
				}}, nil)
				mockStorage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{{
					Metadata: &v1.Metadata{Name: "test-registry"},
					Spec: &v1.ModelRegistrySpec{
						Type: v1.HuggingFaceModelRegistryType,
						Url:  "https://huggingface.co",
					},
					Status: &v1.ModelRegistryStatus{Phase: v1.ModelRegistryPhaseCONNECTED},
				}}, nil)

				// Mock dashboard service - no existing applications
				mockDashboard.On("GetServeApplications").Return(&dashboard.RayServeApplicationsResponse{
					Applications: map[string]dashboard.RayServeApplicationStatus{},
				}, nil)

				// Verify the application name is correct when creating new application
				mockDashboard.On("UpdateServeApplications", mock.MatchedBy(func(req dashboard.RayServeApplicationsRequest) bool {
					if len(req.Applications) != 1 {
						return false
					}
					return req.Applications[0].Name == expectedAppName
				})).Return(nil)
			},
			expectError:   false,
			expectedPhase: v1.EndpointPhaseRUNNING,
		},
		{
			name: "CreateEndpoint with existing application update",
			setupMock: func(mockDashboard *dashboardmocks.MockDashboardService, mockStorage *storagemocks.MockStorage) {
				// Mock storage dependencies
				mockStorage.On("ListCluster", mock.Anything).Return([]v1.Cluster{{
					Metadata: &v1.Metadata{Name: "test-cluster"},
				}}, nil)
				mockStorage.On("ListEngine", mock.Anything).Return([]v1.Engine{{
					Metadata: &v1.Metadata{Name: "vllm"},
					Spec: &v1.EngineSpec{
						Versions: []*v1.EngineVersion{{Version: "0.5.0"}},
					},
					Status: &v1.EngineStatus{Phase: v1.EnginePhaseCreated},
				}}, nil)
				mockStorage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{{
					Metadata: &v1.Metadata{Name: "test-registry"},
					Spec: &v1.ModelRegistrySpec{
						Type: v1.HuggingFaceModelRegistryType,
						Url:  "https://huggingface.co",
					},
					Status: &v1.ModelRegistryStatus{Phase: v1.ModelRegistryPhaseCONNECTED},
				}}, nil)

				// Mock dashboard service - existing application with same name but different config
				existingApp := &dashboard.RayServeApplication{
					Name:        expectedAppName,
					RoutePrefix: "/old/prefix",
					ImportPath:  "old.import.path",
					Args:        map[string]interface{}{"old": "config"},
				}
				mockDashboard.On("GetServeApplications").Return(&dashboard.RayServeApplicationsResponse{
					Applications: map[string]dashboard.RayServeApplicationStatus{
						expectedAppName: {
							Status:            "RUNNING",
							DeployedAppConfig: existingApp,
						},
					},
				}, nil)

				// Verify the application name remains consistent when updating
				mockDashboard.On("UpdateServeApplications", mock.MatchedBy(func(req dashboard.RayServeApplicationsRequest) bool {
					if len(req.Applications) != 1 {
						return false
					}
					return req.Applications[0].Name == expectedAppName
				})).Return(nil)
			},
			expectError:   false,
			expectedPhase: v1.EndpointPhaseRUNNING,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockDashboard := dashboardmocks.NewMockDashboardService(t)
			mockStorage := storagemocks.NewMockStorage(t)

			if tt.setupMock != nil {
				tt.setupMock(mockDashboard, mockStorage)
			}

			mockClusterService := &clustermocks.MockClusterManager{}
			mockClusterService.On("GetDashboardService", mock.Anything).Return(mockDashboard, nil)

			o := &RayOrchestrator{
				clusterHelper: mockClusterService,
				storage:       mockStorage,
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
				opTimeout: OperationConfig{
					CommonTimeout: 600000000000, // 10 minutes
				},
			}

			status, err := o.CreateEndpoint(endpoint)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, status)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, status)
				assert.Equal(t, tt.expectedPhase, status.Phase)
			}

			mockDashboard.AssertExpectations(t)
			mockStorage.AssertExpectations(t)
		})
	}
}
