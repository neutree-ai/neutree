package orchestrator

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"go.openly.dev/pointy"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	acceleratormocks "github.com/neutree-ai/neutree/internal/accelerator/mocks"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	dashboardmocks "github.com/neutree-ai/neutree/internal/ray/dashboard/mocks"
	"github.com/neutree-ai/neutree/internal/util"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func intPtr(i int) *int { return &i }

func newTestRayOrchestratorCtx(s *storagemocks.MockStorage, dashboardService *dashboardmocks.MockDashboardService, endpoint *v1.Endpoint) (*RayOrchestrator, *OrchestratorContext) {
	cluster := &v1.Cluster{
		Metadata: &v1.Metadata{Name: "test-cluster"},
		Spec: &v1.ClusterSpec{
			Type: v1.SSHClusterType,
		},
		Status: &v1.ClusterStatus{
			DashboardURL: "http://127.0.0.1:8265",
			Phase:        v1.ClusterPhaseRunning,
		},
	}

	engine := &v1.Engine{
		Metadata: &v1.Metadata{Name: "vllm"},
		Spec: &v1.EngineSpec{
			Versions: []*v1.EngineVersion{{Version: "0.5.0"}},
		},
		Status: &v1.EngineStatus{Phase: v1.EnginePhaseCreated},
	}
	modelRegistry := &v1.ModelRegistry{
		Metadata: &v1.Metadata{Name: "test-registry"},
		Spec: &v1.ModelRegistrySpec{
			Type: v1.HuggingFaceModelRegistryType,
			Url:  "https://huggingface.co",
		},
		Status: &v1.ModelRegistryStatus{Phase: v1.ModelRegistryPhaseCONNECTED},
	}

	o := &RayOrchestrator{
		cluster: cluster,
		storage: s,
	}

	ctx := &OrchestratorContext{
		Cluster:       cluster,
		Engine:        engine,
		ModelRegistry: modelRegistry,
		Endpoint:      endpoint,
		rayService:    dashboardService,
		logger:        klog.LoggerWithValues(klog.Background(), "endpoint", endpoint.Metadata.WorkspaceName()),
	}

	return o, ctx

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
				CPU:         pointy.String("1.0"),
				GPU:         pointy.String("1.0"),
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
		testFunc    func(o *RayOrchestrator, ctx *OrchestratorContext) error
		expectError bool
	}{
		{
			name: "CreateEndpoint uses consistent app name",
			setupMock: func(mockDashboard *dashboardmocks.MockDashboardService, mockStorage *storagemocks.MockStorage) {

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
			testFunc: func(o *RayOrchestrator, ctx *OrchestratorContext) error {
				err := o.createOrUpdate(ctx)
				return err
			},
			expectError: false,
		},
		{
			name: "DeleteEndpoint uses consistent app name",
			setupMock: func(mockDashboard *dashboardmocks.MockDashboardService, mockStorage *storagemocks.MockStorage) {
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
			testFunc: func(o *RayOrchestrator, ctx *OrchestratorContext) error {
				err := o.deleteEndpoint(ctx)
				return err
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
					Proxies: map[string]dashboard.ProxyStatus{
						"proxy-actor": {
							Status: dashboard.ProxyStatusHealthy,
						},
					},
				}, nil)
			},
			testFunc: func(o *RayOrchestrator, ctx *OrchestratorContext) error {
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

			o, ctx := newTestRayOrchestratorCtx(mockStorage, mockDashboard, endpoint)

			err := tt.testFunc(o, ctx)

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

func TestRayOrchestrator_createOrUpdateEndpoint_ApplicationNameConsistency(t *testing.T) {
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
				CPU:         pointy.String("1.0"),
				GPU:         pointy.String("1.0"),
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
		expectError bool
	}{
		{
			name: "CreateEndpoint with new application",
			setupMock: func(mockDashboard *dashboardmocks.MockDashboardService, mockStorage *storagemocks.MockStorage) {
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
			expectError: false,
		},
		{
			name: "CreateEndpoint with existing application update",
			setupMock: func(mockDashboard *dashboardmocks.MockDashboardService, mockStorage *storagemocks.MockStorage) {
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

			o, ctx := newTestRayOrchestratorCtx(mockStorage, mockDashboard, endpoint)

			err := o.createOrUpdate(ctx)

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

func TestRayOrchestrator_deleteEndpoint(t *testing.T) {
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
				CPU:         pointy.String("1.0"),
				GPU:         pointy.String("1.0"),
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
		name        string
		setupMock   func(*dashboardmocks.MockDashboardService, *storagemocks.MockStorage)
		expectError bool
	}{
		{
			name: "deleteEndpoint with no existing application",
			setupMock: func(mockDashboard *dashboardmocks.MockDashboardService, mockStorage *storagemocks.MockStorage) {
				mockDashboard.On("GetServeApplications").Return(&dashboard.RayServeApplicationsResponse{
					Applications: map[string]dashboard.RayServeApplicationStatus{},
				}, nil)
			},
			expectError: false,
		},
		{
			name: "deleteEndpoint with existing application",
			setupMock: func(mockDashboard *dashboardmocks.MockDashboardService, mockStorage *storagemocks.MockStorage) {
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
			expectError: false,
		},
		{
			name: "UpdateServeApplications should ignore the application which deployedConfig is nil",
			setupMock: func(mockDashboard *dashboardmocks.MockDashboardService, mockStorage *storagemocks.MockStorage) {
				appName := EndpointToServeApplicationName(endpoint)
				mockDashboard.On("GetServeApplications").Return(&dashboard.RayServeApplicationsResponse{
					Applications: map[string]dashboard.RayServeApplicationStatus{
						appName: {
							Status: "RUNNING",
						},
						"test": {
							Status: "DELETING",
						},
					},
				}, nil)
				mockDashboard.On("UpdateServeApplications", mock.MatchedBy(func(req dashboard.RayServeApplicationsRequest) bool {
					return len(req.Applications) == 0
				})).Return(nil)
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

			o, ctx := newTestRayOrchestratorCtx(mockStorage, mockDashboard, endpoint)

			err := o.deleteEndpoint(ctx)
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

func TestEndpointToApplication_NilDeploymentOptions(t *testing.T) {
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
			Replicas: v1.ReplicaSpec{
				Num: pointy.Int(1),
			},
			DeploymentOptions: nil,
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
	assert.Equal(t, "production_chat-model", app.Name)

	// Verify deployment_options was populated correctly despite nil input
	deploymentOptions, ok := app.Args["deployment_options"].(map[string]interface{})
	assert.True(t, ok)
	assert.NotNil(t, deploymentOptions["backend"])
	assert.NotNil(t, deploymentOptions["controller"])
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

func TestEndpointToApplication_SchedulerAliasRoundrobinToPow2(t *testing.T) {
	endpoint := &v1.Endpoint{
		Metadata: &v1.Metadata{
			Name:      "ep",
			Workspace: "ws",
		},
		Spec: &v1.EndpointSpec{
			Engine: &v1.EndpointEngineSpec{
				Engine:  "vllm",
				Version: "v0.8.5",
			},
			Model: &v1.ModelSpec{
				Name:    "m",
				Version: "v1",
				Task:    "text-generation",
			},
			Resources: &v1.ResourceSpec{},
			Replicas:  v1.ReplicaSpec{Num: intPtr(1)},
			DeploymentOptions: map[string]interface{}{
				"scheduler": map[string]interface{}{
					"type": "roundrobin",
				},
			},
			Env: map[string]string{},
		},
	}

	cluster := &v1.Cluster{}
	modelRegistry := &v1.ModelRegistry{
		Spec: &v1.ModelRegistrySpec{
			Type: v1.BentoMLModelRegistryType,
			Url:  "",
		},
	}

	app, err := EndpointToApplication(endpoint, cluster, modelRegistry, nil)
	assert.NoError(t, err)

	deploymentOptions := app.Args["deployment_options"].(map[string]interface{})
	scheduler := deploymentOptions["scheduler"].(map[string]interface{})
	assert.Equal(t, "pow2", scheduler["type"])
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
					Config: &v1.ClusterConfig{
						KubernetesConfig: &v1.KubernetesClusterConfig{},
						ModelCaches: []v1.ModelCache{
							{
								Name:     "test-cache",
								HostPath: &corev1.HostPathVolumeSource{},
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
					Config: &v1.ClusterConfig{
						KubernetesConfig: &v1.KubernetesClusterConfig{},
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
				"version":       "", // Empty version for HuggingFace to use default branch
				"file":          "model.safetensors",
				"task":          v1.TextGenerationModelTask,
				"serve_name":    "llama-2-7b",
				"path":          filepath.Join(v1.DefaultSSHClusterModelCacheMountPath, v1.DefaultModelCacheRelativePath, "llama-2-7b"),
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

func TestEndpointToApplication_setEngineSpecialEnv(t *testing.T) {
	tests := []struct {
		name            string
		endpoint        *v1.Endpoint
		deployedCluster *v1.Cluster
		expectedEnvs    map[string]string
	}{
		{
			name: "endpoint with nil engine",
			endpoint: &v1.Endpoint{
				Metadata: &v1.Metadata{
					Workspace: "default",
					Name:      "llama-endpoint",
				},
				Spec: &v1.EndpointSpec{},
			},
			deployedCluster: &v1.Cluster{Spec: &v1.ClusterSpec{Version: "v1.0.0"}},
			expectedEnvs:    map[string]string{},
		},
		{
			name: "endpoint with llama-cpp engine",
			endpoint: &v1.Endpoint{
				Metadata: &v1.Metadata{
					Workspace: "default",
					Name:      "llama-endpoint",
				},
				Spec: &v1.EndpointSpec{
					Engine: &v1.EndpointEngineSpec{
						Engine: "llama-cpp",
					},
				},
			},
			deployedCluster: &v1.Cluster{Spec: &v1.ClusterSpec{Version: "v1.0.0"}},
			expectedEnvs:    map[string]string{},
		},
		{
			name: "vllm engine on old cluster sets VLLM_SKIP_P2P_CHECK",
			endpoint: &v1.Endpoint{
				Metadata: &v1.Metadata{
					Workspace: "default",
					Name:      "vllm-endpoint",
				},
				Spec: &v1.EndpointSpec{
					Engine: &v1.EndpointEngineSpec{
						Engine: "vllm",
					},
				},
			},
			deployedCluster: &v1.Cluster{Spec: &v1.ClusterSpec{Version: "v1.0.0"}},
			expectedEnvs: map[string]string{
				"VLLM_SKIP_P2P_CHECK": "1",
			},
		},
		{
			name: "vllm engine on new cluster does not set VLLM_SKIP_P2P_CHECK",
			endpoint: &v1.Endpoint{
				Metadata: &v1.Metadata{
					Workspace: "default",
					Name:      "vllm-endpoint",
				},
				Spec: &v1.EndpointSpec{
					Engine: &v1.EndpointEngineSpec{
						Engine: "vllm",
					},
				},
			},
			deployedCluster: &v1.Cluster{Spec: &v1.ClusterSpec{Version: "v1.0.1"}},
			expectedEnvs:    map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			applicationEnvs := map[string]string{}
			setEngineSpecialEnv(tt.endpoint, tt.deployedCluster, applicationEnvs)
			assert.Equal(t, tt.expectedEnvs, applicationEnvs)
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

func TestRayOrchestrator_GetEndpointStatus(t *testing.T) {
	newEndpoint := func() *v1.Endpoint {
		return &v1.Endpoint{
			Metadata: &v1.Metadata{
				Workspace: "production",
				Name:      "chat-model",
			},
			Spec: &v1.EndpointSpec{
				Replicas: v1.ReplicaSpec{
					Num: pointy.Int(1),
				},
			},
		}
	}

	applicationName := "production_chat-model"

	tests := []struct {
		name           string
		inputEndpoint  func() *v1.Endpoint
		setupMock      func(*dashboardmocks.MockDashboardService)
		expectedPhase  v1.EndpointPhase
		expectErrorMsg string
		expectError    bool
	}{
		{
			name: "return error if application fetch fails",
			inputEndpoint: func() *v1.Endpoint {
				return newEndpoint()
			},
			setupMock: func(mockDashboard *dashboardmocks.MockDashboardService) {
				mockDashboard.On("GetServeApplications").Return(&dashboard.RayServeApplicationsResponse{
					Applications: map[string]dashboard.RayServeApplicationStatus{},
				}, assert.AnError)
			},
			expectError: true,
		},
		{
			name: "return Deleted for non-existing application",
			inputEndpoint: func() *v1.Endpoint {
				ep := newEndpoint()
				ep.Metadata.DeletionTimestamp = time.Now().Format(time.RFC3339Nano)
				return ep
			},
			setupMock: func(mockDashboard *dashboardmocks.MockDashboardService) {
				mockDashboard.On("GetServeApplications").Return(&dashboard.RayServeApplicationsResponse{
					Applications: map[string]dashboard.RayServeApplicationStatus{},
				}, nil)
			},
			expectedPhase: v1.EndpointPhaseDELETED,
			expectError:   false,
		},
		{
			name: "return Deleting for existing application when deletion timestamp is set",
			inputEndpoint: func() *v1.Endpoint {
				ep := newEndpoint()
				ep.Metadata.DeletionTimestamp = time.Now().Format(time.RFC3339Nano)
				return ep
			},
			setupMock: func(mockDashboard *dashboardmocks.MockDashboardService) {
				existingApp := &dashboard.RayServeApplication{
					Name:        applicationName,
					RoutePrefix: "/production/chat-model",
					ImportPath:  "old.import.path",
					Args:        map[string]interface{}{"old": "config"},
				}

				mockDashboard.On("GetServeApplications").Return(&dashboard.RayServeApplicationsResponse{
					Applications: map[string]dashboard.RayServeApplicationStatus{
						applicationName: {
							Status:            "RUNNING",
							DeployedAppConfig: existingApp,
						},
					},
				}, nil)
			},
			expectedPhase:  v1.EndpointPhaseDELETING,
			expectErrorMsg: "Endpoint deleting in progress",
			expectError:    false,
		},
		{
			name: "return Deploying for endpoint with zero replicas but application still existing ",
			inputEndpoint: func() *v1.Endpoint {
				ep := newEndpoint()
				ep.Spec.Replicas.Num = pointy.Int(0)
				return ep
			},
			setupMock: func(mockDashboard *dashboardmocks.MockDashboardService) {
				existingApp := &dashboard.RayServeApplication{
					Name:        applicationName,
					RoutePrefix: "/production/chat-model",
					ImportPath:  "old.import.path",
					Args:        map[string]interface{}{"old": "config"},
				}

				mockDashboard.On("GetServeApplications").Return(&dashboard.RayServeApplicationsResponse{
					Applications: map[string]dashboard.RayServeApplicationStatus{
						applicationName: {
							Status:            "RUNNING",
							DeployedAppConfig: existingApp,
						},
					},
				}, nil)
			},
			expectedPhase:  v1.EndpointPhaseDEPLOYING,
			expectErrorMsg: "Endpoint pausing in progress",
			expectError:    false,
		},
		{
			name: "return Paused for endpoint with zero replicas and application not existing ",
			inputEndpoint: func() *v1.Endpoint {
				ep := newEndpoint()
				ep.Spec.Replicas.Num = pointy.Int(0)
				return ep
			},
			setupMock: func(mockDashboard *dashboardmocks.MockDashboardService) {
				mockDashboard.On("GetServeApplications").Return(&dashboard.RayServeApplicationsResponse{}, nil)
			},
			expectedPhase: v1.EndpointPhasePAUSED,
			expectError:   false,
		},
		{
			name: "return Deploying for none application",
			inputEndpoint: func() *v1.Endpoint {
				return newEndpoint()
			},
			setupMock: func(mockDashboard *dashboardmocks.MockDashboardService) {
				mockDashboard.On("GetServeApplications").Return(&dashboard.RayServeApplicationsResponse{}, nil)
			},
			expectedPhase:  v1.EndpointPhaseDEPLOYING,
			expectErrorMsg: "Endpoint deploying in progress",
			expectError:    false,
		},
		{
			name: "return Deploying for application is in DEPLOYING status",
			inputEndpoint: func() *v1.Endpoint {
				return newEndpoint()
			},
			setupMock: func(mockDashboard *dashboardmocks.MockDashboardService) {
				existingApp := &dashboard.RayServeApplication{
					Name:        applicationName,
					RoutePrefix: "/production/chat-model",
					ImportPath:  "old.import.path",
					Args:        map[string]interface{}{"old": "config"},
				}

				mockDashboard.On("GetServeApplications").Return(&dashboard.RayServeApplicationsResponse{
					Applications: map[string]dashboard.RayServeApplicationStatus{
						applicationName: {
							Status:            "DEPLOYING",
							DeployedAppConfig: existingApp,
						},
					},
				}, nil)
			},
			expectedPhase:  v1.EndpointPhaseDEPLOYING,
			expectErrorMsg: "Endpoint deploying in progress",
			expectError:    false,
		},
		{
			name: "return Deploying for application is in NOT_STARTED status",
			inputEndpoint: func() *v1.Endpoint {
				return newEndpoint()
			},
			setupMock: func(mockDashboard *dashboardmocks.MockDashboardService) {
				existingApp := &dashboard.RayServeApplication{
					Name:        applicationName,
					RoutePrefix: "/production/chat-model",
					ImportPath:  "old.import.path",
					Args:        map[string]interface{}{"old": "config"},
				}

				mockDashboard.On("GetServeApplications").Return(&dashboard.RayServeApplicationsResponse{
					Applications: map[string]dashboard.RayServeApplicationStatus{
						applicationName: {
							Status:            "NOT_STARTED",
							DeployedAppConfig: existingApp,
						},
					},
				}, nil)
			},
			expectedPhase:  v1.EndpointPhaseDEPLOYING,
			expectErrorMsg: "Endpoint deploying in progress",
			expectError:    false,
		},
		{
			name: "return failed for application is in DEPLOY_FAILED status",
			inputEndpoint: func() *v1.Endpoint {
				return newEndpoint()
			},
			setupMock: func(mockDashboard *dashboardmocks.MockDashboardService) {
				existingApp := &dashboard.RayServeApplication{
					Name:        applicationName,
					RoutePrefix: "/production/chat-model",
					ImportPath:  "old.import.path",
					Args:        map[string]interface{}{"old": "config"},
				}

				mockDashboard.On("GetServeApplications").Return(&dashboard.RayServeApplicationsResponse{
					Applications: map[string]dashboard.RayServeApplicationStatus{
						applicationName: {
							Status:            "DEPLOY_FAILED",
							DeployedAppConfig: existingApp,
						},
					},
				}, nil)
			},
			expectedPhase:  v1.EndpointPhaseFAILED,
			expectErrorMsg: "Endpoint failed",
		},
		{
			name: "return failed for application is in UNHEALTHY status",
			inputEndpoint: func() *v1.Endpoint {
				return newEndpoint()
			},
			setupMock: func(mockDashboard *dashboardmocks.MockDashboardService) {
				existingApp := &dashboard.RayServeApplication{
					Name:        applicationName,
					RoutePrefix: "/production/chat-model",
					ImportPath:  "old.import.path",
					Args:        map[string]interface{}{"old": "config"},
				}

				mockDashboard.On("GetServeApplications").Return(&dashboard.RayServeApplicationsResponse{
					Applications: map[string]dashboard.RayServeApplicationStatus{
						applicationName: {
							Status:            "UNHEALTHY",
							DeployedAppConfig: existingApp,
						},
					},
				}, nil)
			},
			expectedPhase:  v1.EndpointPhaseFAILED,
			expectErrorMsg: "Endpoint failed",
		},
		{
			name: "return Running for application is in Running status and proxy is healthy",
			inputEndpoint: func() *v1.Endpoint {
				return newEndpoint()
			},
			setupMock: func(mockDashboard *dashboardmocks.MockDashboardService) {
				existingApp := &dashboard.RayServeApplication{
					Name:        applicationName,
					RoutePrefix: "/production/chat-model",
					ImportPath:  "old.import.path",
					Args:        map[string]interface{}{"old": "config"},
				}

				mockDashboard.On("GetServeApplications").Return(&dashboard.RayServeApplicationsResponse{
					Applications: map[string]dashboard.RayServeApplicationStatus{
						applicationName: {
							Status:            "RUNNING",
							DeployedAppConfig: existingApp,
						},
					},
					Proxies: map[string]dashboard.ProxyStatus{
						"proxy-actor": {
							Status: dashboard.ProxyStatusHealthy,
						},
					},
				}, nil)
			},
			expectedPhase: v1.EndpointPhaseRUNNING,
			expectError:   false,
		},
		{
			name: "return Deploying for application is in Running status but none proxy",
			inputEndpoint: func() *v1.Endpoint {
				return newEndpoint()
			},
			setupMock: func(mockDashboard *dashboardmocks.MockDashboardService) {
				existingApp := &dashboard.RayServeApplication{
					Name:        applicationName,
					RoutePrefix: "/production/chat-model",
					ImportPath:  "old.import.path",
					Args:        map[string]interface{}{"old": "config"},
				}

				mockDashboard.On("GetServeApplications").Return(&dashboard.RayServeApplicationsResponse{
					Applications: map[string]dashboard.RayServeApplicationStatus{
						applicationName: {
							Status:            "RUNNING",
							DeployedAppConfig: existingApp,
						},
					},
				}, nil)
			},
			expectedPhase:  v1.EndpointPhaseDEPLOYING,
			expectErrorMsg: "Endpoint deploying in progress",
			expectError:    false,
		},
		{
			name: "return Deploying for application is in Running status but proxy is unhealthy",
			inputEndpoint: func() *v1.Endpoint {
				return newEndpoint()
			},
			setupMock: func(mockDashboard *dashboardmocks.MockDashboardService) {
				existingApp := &dashboard.RayServeApplication{
					Name:        applicationName,
					RoutePrefix: "/production/chat-model",
					ImportPath:  "old.import.path",
					Args:        map[string]interface{}{"old": "config"},
				}

				mockDashboard.On("GetServeApplications").Return(&dashboard.RayServeApplicationsResponse{
					Applications: map[string]dashboard.RayServeApplicationStatus{
						applicationName: {
							Status:            "Running",
							DeployedAppConfig: existingApp,
						},
					},
					Proxies: map[string]dashboard.ProxyStatus{
						"proxy-actor": {
							Status: dashboard.ProxyStatusUnhealthy,
						},
					},
				}, nil)
			},
			expectedPhase:  v1.EndpointPhaseDEPLOYING,
			expectErrorMsg: "Endpoint deploying in progress",
			expectError:    false,
		},
		{
			name: "return Deploying for status not catched case",
			inputEndpoint: func() *v1.Endpoint {
				return newEndpoint()
			},
			setupMock: func(mockDashboard *dashboardmocks.MockDashboardService) {
				existingApp := &dashboard.RayServeApplication{
					Name:        applicationName,
					RoutePrefix: "/production/chat-model",
					ImportPath:  "old.import.path",
					Args:        map[string]interface{}{"old": "config"},
				}

				mockDashboard.On("GetServeApplications").Return(&dashboard.RayServeApplicationsResponse{
					Applications: map[string]dashboard.RayServeApplicationStatus{
						applicationName: {
							Status:            "UNDEFINED_STATUS",
							DeployedAppConfig: existingApp,
						},
					},
				}, nil)
			},
			expectedPhase:  v1.EndpointPhaseDEPLOYING,
			expectErrorMsg: "Endpoint deploying in progress",
			expectError:    false,
		},
		{
			name: "should merge errormsgs from different checks when ray serve application is not in Running status",
			inputEndpoint: func() *v1.Endpoint {
				ep := newEndpoint()
				return ep
			},
			setupMock: func(mockDashboard *dashboardmocks.MockDashboardService) {
				existingApp := &dashboard.RayServeApplication{
					Name:        applicationName,
					RoutePrefix: "/production/chat-model",
					ImportPath:  "old.import.path",
					Args:        map[string]interface{}{"old": "config"},
				}

				mockDashboard.On("GetServeApplications").Return(&dashboard.RayServeApplicationsResponse{
					Applications: map[string]dashboard.RayServeApplicationStatus{
						applicationName: {
							Status:            "DEPLOYING",
							DeployedAppConfig: existingApp,
							Deployments: map[string]dashboard.Deployment{
								"BACKEND": {
									Name:    "BACKEND",
									Message: "backend deployment is error",
								},
							},
						},
					},
				}, nil)
			},
			expectedPhase:  v1.EndpointPhaseDEPLOYING,
			expectErrorMsg: "Deployment BACKEND: backend deployment is error",
			expectError:    false,
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
						Config:  &v1.ClusterConfig{},
					},
					Status: &v1.ClusterStatus{
						Initialized:  true,
						DashboardURL: "http://ray-dashboard.example.com:8265",
					},
				},
			}

			status, err := o.GetEndpointStatus(tt.inputEndpoint())
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.Equal(t, tt.expectedPhase, status.Phase)
				if tt.expectErrorMsg != "" {
					assert.Contains(t, status.ErrorMessage, tt.expectErrorMsg)
				}
				assert.NoError(t, err)
			}

			mockDashboard.AssertExpectations(t)
		})
	}
}
