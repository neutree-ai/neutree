package orchestrator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"go.openly.dev/pointy"

	v1 "github.com/neutree-ai/neutree/api/v1"
	acceleratormocks "github.com/neutree-ai/neutree/internal/accelerator/mocks"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	dashboardmocks "github.com/neutree-ai/neutree/internal/ray/dashboard/mocks"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

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
				Accelerator: make(map[string]string),
			},
			Replicas: v1.ReplicaSpec{
				Num: pointy.Int(1),
			},
			DeploymentOptions: map[string]interface{}{},
			Variables:         map[string]interface{}{},
		},
	}

	expectedAppName := EndpointToServeApplicationName(endpoint)
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

			dashboard.NewDashboardService = func(dashboardUrl string) dashboard.DashboardService {
				return mockDashboard
			}

			o := &RayOrchestrator{
				storage: mockStorage,
				cluster: &v1.Cluster{
					Metadata: &v1.Metadata{Name: "test-cluster"},
					Spec: &v1.ClusterSpec{
						Version: "v1.0.0",
						Config:  map[string]interface{}{},
					},
					Status: &v1.ClusterStatus{
						Initialized:  true,
						DashboardURL: "http://127.0.0.1:8265",
					},
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
				Accelerator: make(map[string]string),
			},
			Replicas: v1.ReplicaSpec{
				Num: pointy.Int(1),
			},
			DeploymentOptions: map[string]interface{}{},
			Variables:         map[string]interface{}{},
		},
	}

	expectedAppName := EndpointToServeApplicationName(endpoint)
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

			dashboard.NewDashboardService = func(dashboardUrl string) dashboard.DashboardService {
				return mockDashboard
			}

			o := &RayOrchestrator{
				storage: mockStorage,
				cluster: &v1.Cluster{
					Metadata: &v1.Metadata{Name: "test-cluster"},
					Spec: &v1.ClusterSpec{
						Version: "v1.0.0",
						Config:  map[string]interface{}{},
					},
					Status: &v1.ClusterStatus{
						Initialized:  true,
						DashboardURL: "http://127.0.0.1:8265",
					},
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

func TestEndpointToServeApplicationName(t *testing.T) {
	tests := []struct {
		name     string
		endpoint *v1.Endpoint
		expected string
	}{
		{
			name: "basic endpoint with workspace and name",
			endpoint: &v1.Endpoint{
				Metadata: &v1.Metadata{
					Workspace: "production",
					Name:      "chat-model",
				},
			},
			expected: "production_chat-model",
		},
		{
			name: "default workspace",
			endpoint: &v1.Endpoint{
				Metadata: &v1.Metadata{
					Workspace: "default",
					Name:      "test-endpoint",
				},
			},
			expected: "default_test-endpoint",
		},
		{
			name: "workspace with special characters",
			endpoint: &v1.Endpoint{
				Metadata: &v1.Metadata{
					Workspace: "dev-test",
					Name:      "model-v1",
				},
			},
			expected: "dev-test_model-v1",
		},
		{
			name: "empty workspace",
			endpoint: &v1.Endpoint{
				Metadata: &v1.Metadata{
					Workspace: "",
					Name:      "endpoint",
				},
			},
			expected: "_endpoint",
		},
		{
			name: "empty name",
			endpoint: &v1.Endpoint{
				Metadata: &v1.Metadata{
					Workspace: "workspace",
					Name:      "",
				},
			},
			expected: "workspace_",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := EndpointToServeApplicationName(tt.endpoint)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestEndpointToApplication_ApplicationNameConsistency(t *testing.T) {
	endpoint := &v1.Endpoint{
		Metadata: &v1.Metadata{
			Workspace: "production",
			Name:      "chat-model",
		},
		Spec: &v1.EndpointSpec{
			Engine: &v1.EndpointEngineSpec{
				Engine:  "vllm",
				Version: "0.5.0",
			},
			Resources: &v1.ResourceSpec{
				Accelerator: map[string]string{},
			},
			DeploymentOptions: map[string]interface{}{},
			Model: &v1.ModelSpec{
				Name: "test-model",
			},
		},
	}

	modelRegistry := &v1.ModelRegistry{
		Spec: &v1.ModelRegistrySpec{
			Type: v1.HuggingFaceModelRegistryType,
		},
	}

	mgr := &acceleratormocks.MockManager{}

	app, err := EndpointToApplication(endpoint, modelRegistry, mgr)
	assert.NoError(t, err)
	// Verify that the application name matches the naming function
	expectedName := EndpointToServeApplicationName(endpoint)
	assert.Equal(t, expectedName, app.Name)
	assert.Equal(t, "production_chat-model", app.Name)
}

func TestEndpointToApplication_RouteConsistency(t *testing.T) {
	endpoint := &v1.Endpoint{
		Metadata: &v1.Metadata{
			Workspace: "production",
			Name:      "chat-model",
		},
		Spec: &v1.EndpointSpec{
			Engine: &v1.EndpointEngineSpec{
				Engine:  "vllm",
				Version: "0.5.0",
			},
			Resources: &v1.ResourceSpec{
				Accelerator: map[string]string{},
			},
			DeploymentOptions: map[string]interface{}{},
			Model: &v1.ModelSpec{
				Name: "test-model",
			},
		},
	}

	modelRegistry := &v1.ModelRegistry{
		Spec: &v1.ModelRegistrySpec{
			Type: v1.HuggingFaceModelRegistryType,
		},
	}

	app, err := EndpointToApplication(endpoint, modelRegistry, nil)
	assert.NoError(t, err)

	// Verify that the route prefix includes workspace
	assert.Equal(t, "/production/chat-model", app.RoutePrefix)
}

func TestFormatServiceURL_WorkspaceInURL(t *testing.T) {
	cluster := &v1.Cluster{
		Status: &v1.ClusterStatus{
			DashboardURL: "http://ray-dashboard.example.com:8265",
		},
	}

	endpoint := &v1.Endpoint{
		Metadata: &v1.Metadata{
			Workspace: "production",
			Name:      "chat-model",
		},
	}

	url, err := FormatServiceURL(cluster, endpoint)
	assert.NoError(t, err)
	assert.Equal(t, "http://ray-dashboard.example.com:8000/production/chat-model", url)
}
