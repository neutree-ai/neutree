package orchestrator

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"go.openly.dev/pointy"
	corev1 "k8s.io/api/core/v1"

	v1 "github.com/neutree-ai/neutree/api/v1"
	acceleratormocks "github.com/neutree-ai/neutree/internal/accelerator/mocks"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	dashboardmocks "github.com/neutree-ai/neutree/internal/ray/dashboard/mocks"
	"github.com/neutree-ai/neutree/internal/util"
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

func TestRayOrchestrator_CreateEndpoint_WithZeroReplicas(t *testing.T) {
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
				Num: pointy.Int(0),
			},
			DeploymentOptions: map[string]interface{}{},
			Variables:         map[string]interface{}{},
		},
	}

	tests := []struct {
		name          string
		setupMock     func(*dashboardmocks.MockDashboardService, *storagemocks.MockStorage)
		expectedPhase v1.EndpointPhase
		expectError   bool
	}{
		{
			name: "CreateEndpoint with zero replicas, no existing application",
			setupMock: func(mockDashboard *dashboardmocks.MockDashboardService, mockStorage *storagemocks.MockStorage) {
				// Mock storage dependencies
				mockStorage.On("ListCluster", mock.Anything).Return([]v1.Cluster{{
					Metadata: &v1.Metadata{Name: "test-cluster"},
				}}, nil)
				mockDashboard.On("GetServeApplications").Return(&dashboard.RayServeApplicationsResponse{
					Applications: map[string]dashboard.RayServeApplicationStatus{},
				}, nil)
			},
			expectedPhase: v1.EndpointPhaseRUNNING,
			expectError:   false,
		},
		{
			name: "CreateEndpoint with zero replicas, existing application",
			setupMock: func(mockDashboard *dashboardmocks.MockDashboardService, mockStorage *storagemocks.MockStorage) {
				// Mock storage dependencies
				mockStorage.On("ListCluster", mock.Anything).Return([]v1.Cluster{{
					Metadata: &v1.Metadata{Name: "test-cluster"},
				}}, nil)

				appName := EndpointToServeApplicationName(endpoint)
				// Mock dashboard service - existing application with same name but different config
				existingApp := &dashboard.RayServeApplication{
					Name:        appName,
					RoutePrefix: "/old/prefix",
					ImportPath:  "old.import.path",
					Args:        map[string]interface{}{"old": "config"},
				}

				mockDashboard.On("GetServeApplications").Return(&dashboard.RayServeApplicationsResponse{
					Applications: map[string]dashboard.RayServeApplicationStatus{
						appName: {
							Status:            "RUNNING",
							DeployedAppConfig: existingApp,
						},
					},
				}, nil)
				// Verify the application name remains consistent when updating
				mockDashboard.On("UpdateServeApplications", mock.MatchedBy(func(req dashboard.RayServeApplicationsRequest) bool {
					return len(req.Applications) == 0
				})).Return(nil)
			},
			expectedPhase: v1.EndpointPhaseRUNNING,
			expectError:   false,
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

	deployedCluster := &v1.Cluster{}

	mgr := &acceleratormocks.MockManager{}

	app, err := EndpointToApplication(endpoint, deployedCluster, modelRegistry, mgr)
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

	app, err := EndpointToApplication(endpoint, &v1.Cluster{}, modelRegistry, nil)
	assert.NoError(t, err)

	// Verify that the route prefix includes workspace
	assert.Equal(t, "/production/chat-model", app.RoutePrefix)
}

func TestEndpointToApplication_setModelArgs(t *testing.T) {
	tests := []struct {
		name              string
		endpoint          *v1.Endpoint
		modelRegistry     *v1.ModelRegistry
		cluster           *v1.Cluster
		expectedModelArgs map[string]string
		expectedEnvs      map[string]string
		wantErr           bool
	}{
		{
			name: "BentoML modelRegistry - specific version",
			modelRegistry: &v1.ModelRegistry{
				Metadata: &v1.Metadata{
					Name: "bentoml-registry",
				},
				Spec: &v1.ModelRegistrySpec{
					Type: v1.BentoMLModelRegistryType,
					Url:  "nfs://192.168.1.100/bentoml",
				},
			},
			endpoint: &v1.Endpoint{
				Metadata: &v1.Metadata{
					Workspace: "default",
					Name:      "llama-endpoint",
				},
				Spec: &v1.EndpointSpec{
					Model: &v1.ModelSpec{
						Name:    "llama-2-7b",
						Version: "v1.0",
						File:    "model.safetensors",
						Task:    v1.TextGenerationModelTask,
					},
					Resources:         &v1.ResourceSpec{},
					Engine:            &v1.EndpointEngineSpec{},
					DeploymentOptions: map[string]interface{}{},
				},
			},
			cluster: &v1.Cluster{},
			expectedModelArgs: map[string]string{
				"registry_type": "bentoml",
				"name":          "llama-2-7b",
				"version":       "v1.0",
				"file":          "model.safetensors",
				"task":          v1.TextGenerationModelTask,
				"serve_name":    "llama-2-7b:v1.0",
				"path":          filepath.Join(v1.DefaultSSHClusterModelCacheMountPath, v1.DefaultModelCacheRelativePath, "llama-2-7b", "v1.0"),
				"registry_path": filepath.Join("/mnt", "default", "llama-endpoint", "models", "llama-2-7b", "v1.0"),
			},
			expectedEnvs: map[string]string{},
			wantErr:      false,
		},
		{
			name: "BentoML modelRegistry - specific version - with cluster model cache",
			modelRegistry: &v1.ModelRegistry{
				Metadata: &v1.Metadata{
					Name: "bentoml-registry",
				},
				Spec: &v1.ModelRegistrySpec{
					Type: v1.BentoMLModelRegistryType,
					Url:  "nfs://192.168.1.100/bentoml",
				},
			},
			endpoint: &v1.Endpoint{
				Metadata: &v1.Metadata{
					Workspace: "default",
					Name:      "llama-endpoint",
				},
				Spec: &v1.EndpointSpec{
					Model: &v1.ModelSpec{
						Name:    "llama-2-7b",
						Version: "v1.0",
						File:    "model.safetensors",
						Task:    v1.TextGenerationModelTask,
					},
					Resources:         &v1.ResourceSpec{},
					Engine:            &v1.EndpointEngineSpec{},
					DeploymentOptions: map[string]interface{}{},
				},
			},
			cluster: &v1.Cluster{
				Spec: &v1.ClusterSpec{
					Config: &v1.KubernetesClusterConfig{
						CommonClusterConfig: v1.CommonClusterConfig{
							ModelCaches: []v1.ModelCache{
								{
									Name:     "test-cache",
									HostPath: &corev1.HostPathVolumeSource{},
								},
							},
						},
					},
				},
			},
			expectedModelArgs: map[string]string{
				"registry_type": "bentoml",
				"name":          "llama-2-7b",
				"version":       "v1.0",
				"file":          "model.safetensors",
				"task":          v1.TextGenerationModelTask,
				"serve_name":    "llama-2-7b:v1.0",
				"path":          filepath.Join(v1.DefaultSSHClusterModelCacheMountPath, "test-cache", "llama-2-7b", "v1.0"),
				"registry_path": filepath.Join("/mnt", "default", "llama-endpoint", "models", "llama-2-7b", "v1.0"),
			},
			expectedEnvs: map[string]string{},
			wantErr:      false,
		},
		{
			name: "BentoML modelRegistry - specific version - with cluster multi model cache - only use the first one",
			modelRegistry: &v1.ModelRegistry{
				Metadata: &v1.Metadata{
					Name: "bentoml-registry",
				},
				Spec: &v1.ModelRegistrySpec{
					Type: v1.BentoMLModelRegistryType,
					Url:  "nfs://192.168.1.100/bentoml",
				},
			},
			endpoint: &v1.Endpoint{
				Metadata: &v1.Metadata{
					Workspace: "default",
					Name:      "llama-endpoint",
				},
				Spec: &v1.EndpointSpec{
					Model: &v1.ModelSpec{
						Name:    "llama-2-7b",
						Version: "v1.0",
						File:    "model.safetensors",
						Task:    v1.TextGenerationModelTask,
					},
					Resources:         &v1.ResourceSpec{},
					Engine:            &v1.EndpointEngineSpec{},
					DeploymentOptions: map[string]interface{}{},
				},
			},
			cluster: &v1.Cluster{
				Spec: &v1.ClusterSpec{
					Config: &v1.KubernetesClusterConfig{
						CommonClusterConfig: v1.CommonClusterConfig{
							ModelCaches: []v1.ModelCache{
								{
									Name:     "test-cache-1",
									HostPath: &corev1.HostPathVolumeSource{},
								},
								{
									Name:     "test-cache-2",
									HostPath: &corev1.HostPathVolumeSource{},
								},
							},
						},
					},
				},
			},
			expectedModelArgs: map[string]string{
				"registry_type": "bentoml",
				"name":          "llama-2-7b",
				"version":       "v1.0",
				"file":          "model.safetensors",
				"task":          v1.TextGenerationModelTask,
				"serve_name":    "llama-2-7b:v1.0",
				"path":          filepath.Join(v1.DefaultSSHClusterModelCacheMountPath, "test-cache-1", "llama-2-7b", "v1.0"),
				"registry_path": filepath.Join("/mnt", "default", "llama-endpoint", "models", "llama-2-7b", "v1.0"),
			},
			expectedEnvs: map[string]string{},
			wantErr:      false,
		},
		{
			name: "HuggingFace modelRegistry - specific version",
			modelRegistry: &v1.ModelRegistry{
				Metadata: &v1.Metadata{
					Name: "huggingface-registry",
				},
				Spec: &v1.ModelRegistrySpec{
					Type:        v1.HuggingFaceModelRegistryType,
					Url:         "https://huggingface.co",
					Credentials: "test-token",
				},
			},
			endpoint: &v1.Endpoint{
				Metadata: &v1.Metadata{
					Workspace: "default",
					Name:      "llama-endpoint",
				},
				Spec: &v1.EndpointSpec{
					Model: &v1.ModelSpec{
						Name:    "llama-2-7b",
						Version: "v1.0",
						File:    "model.safetensors",
						Task:    v1.TextGenerationModelTask,
					},
					Resources:         &v1.ResourceSpec{},
					Engine:            &v1.EndpointEngineSpec{},
					DeploymentOptions: map[string]interface{}{},
				},
			},
			cluster: &v1.Cluster{},
			expectedModelArgs: map[string]string{
				"registry_type": "hugging-face",
				"name":          "llama-2-7b",
				"version":       "v1.0",
				"file":          "model.safetensors",
				"task":          v1.TextGenerationModelTask,
				"serve_name":    "llama-2-7b",
				"path":          filepath.Join(v1.DefaultSSHClusterModelCacheMountPath, v1.DefaultModelCacheRelativePath, "llama-2-7b", "v1.0"),
				"registry_path": "llama-2-7b",
			},
			expectedEnvs: map[string]string{
				v1.HFEndpoint: "https://huggingface.co",
				v1.HFTokenEnv: "test-token",
			},
			wantErr: false,
		},
		{
			name: "HuggingFace modelRegistry - without specific version",
			modelRegistry: &v1.ModelRegistry{
				Metadata: &v1.Metadata{
					Name: "huggingface-registry",
				},
				Spec: &v1.ModelRegistrySpec{
					Type:        v1.HuggingFaceModelRegistryType,
					Url:         "https://huggingface.co",
					Credentials: "test-token",
				},
			},
			endpoint: &v1.Endpoint{
				Metadata: &v1.Metadata{
					Workspace: "default",
					Name:      "llama-endpoint",
				},
				Spec: &v1.EndpointSpec{
					Model: &v1.ModelSpec{
						Name: "llama-2-7b",
						File: "model.safetensors",
						Task: v1.TextGenerationModelTask,
					},
					Resources:         &v1.ResourceSpec{},
					Engine:            &v1.EndpointEngineSpec{},
					DeploymentOptions: map[string]interface{}{},
				},
			},
			cluster: &v1.Cluster{},
			expectedModelArgs: map[string]string{
				"registry_type": "hugging-face",
				"name":          "llama-2-7b",
				"version":       "main",
				"file":          "model.safetensors",
				"task":          v1.TextGenerationModelTask,
				"serve_name":    "llama-2-7b",
				"path":          filepath.Join(v1.DefaultSSHClusterModelCacheMountPath, v1.DefaultModelCacheRelativePath, "llama-2-7b", "main"),
				"registry_path": "llama-2-7b",
			},
			expectedEnvs: map[string]string{
				v1.HFEndpoint: "https://huggingface.co",
				v1.HFTokenEnv: "test-token",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, err := EndpointToApplication(tt.endpoint, tt.cluster, tt.modelRegistry, nil)
			if tt.wantErr {
				assert.Error(t, err)
				return
			} else {
				assert.NoError(t, err)
				modelArgs := app.Args["model"].(map[string]interface{})
				eq, _, err := util.JsonEqual(tt.expectedModelArgs, modelArgs)
				assert.NoError(t, err)
				if !eq {
					t.Errorf("Model args do not match expected.\nGot: %+v\nExpected: %+v", modelArgs, tt.expectedModelArgs)
				}

				envs := app.RuntimeEnv["env_vars"].(map[string]string)
				eq, _, err = util.JsonEqual(tt.expectedEnvs, envs)
				assert.NoError(t, err)
				if !eq {
					t.Errorf("Envs do not match expected.\nGot: %+v\nExpected: %+v", envs, tt.expectedEnvs)
				}
			}
		})
	}
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

func TestRayOrchestrator_GetEndpointStatus_WithZeroReplicas(t *testing.T) {
	tests := []struct {
		name          string
		setupMock     func(*dashboardmocks.MockDashboardService)
		expectedPhase v1.EndpointPhase
		expectError   bool
	}{
		{
			name: "GetEndpointStatus with zero replicas, no existing application",
			setupMock: func(mockDashboard *dashboardmocks.MockDashboardService) {
				mockDashboard.On("GetServeApplications").Return(&dashboard.RayServeApplicationsResponse{
					Applications: map[string]dashboard.RayServeApplicationStatus{},
				}, nil)
			},
			expectedPhase: v1.EndpointPhaseRUNNING,
			expectError:   false,
		},
		{
			name: "GetEndpointStatus with zero replicas, existing application",
			setupMock: func(mockDashboard *dashboardmocks.MockDashboardService) {
				existingApp := &dashboard.RayServeApplication{
					Name:        "production_chat-model",
					RoutePrefix: "/production/chat-model",
					ImportPath:  "old.import.path",
					Args:        map[string]interface{}{"old": "config"},
				}

				mockDashboard.On("GetServeApplications").Return(&dashboard.RayServeApplicationsResponse{
					Applications: map[string]dashboard.RayServeApplicationStatus{
						"production_chat-model": {
							Status:            "RUNNING",
							DeployedAppConfig: existingApp,
						},
					},
				}, nil)
			},
			expectedPhase: v1.EndpointPhasePENDING,
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockDashboard := dashboardmocks.NewMockDashboardService(t)
			if tt.setupMock != nil {
				tt.setupMock(mockDashboard)
			}

			dashboard.NewDashboardService = func(dashboardUrl string) dashboard.DashboardService {
				return mockDashboard
			}

			o := &RayOrchestrator{
				cluster: &v1.Cluster{
					Metadata: &v1.Metadata{Name: "test-cluster"},
					Spec: &v1.ClusterSpec{
						Version: "v1.0.0",
						Config:  map[string]interface{}{},
					},
					Status: &v1.ClusterStatus{
						Initialized:  true,
						DashboardURL: "http://ray-dashboard.example.com:8265",
					},
				},
			}

			endpoint := &v1.Endpoint{
				Metadata: &v1.Metadata{
					Workspace: "production",
					Name:      "chat-model",
				},
				Spec: &v1.EndpointSpec{
					Replicas: v1.ReplicaSpec{
						Num: pointy.Int(0),
					},
				},
			}

			status, err := o.GetEndpointStatus(endpoint)
			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, status)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, status)
				assert.Equal(t, tt.expectedPhase, status.Phase)
			}

			mockDashboard.AssertExpectations(t)
		})
	}
}
