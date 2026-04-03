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
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	dashboardmocks "github.com/neutree-ai/neutree/internal/ray/dashboard/mocks"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/model_registry"
	modelregistrymocks "github.com/neutree-ai/neutree/pkg/model_registry/mocks"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func intPtr(i int) *int { return &i }

func newTestRayOrchestratorCtx(s *storagemocks.MockStorage, dashboardService *dashboardmocks.MockDashboardService, endpoint *v1.Endpoint, acceleratorMgr *acceleratormocks.MockManager) (*RayOrchestrator, *OrchestratorContext) {
	acceleratorType := string(v1.AcceleratorTypeNVIDIAGPU)

	cluster := &v1.Cluster{
		Metadata: &v1.Metadata{Name: "test-cluster"},
		Spec: &v1.ClusterSpec{
			Type: v1.SSHClusterType,
		},
		Status: &v1.ClusterStatus{
			DashboardURL:    "http://127.0.0.1:8265",
			Phase:           v1.ClusterPhaseRunning,
			AcceleratorType: &acceleratorType,
		},
	}

	engine := &v1.Engine{
		Metadata: &v1.Metadata{Name: "vllm"},
		Spec: &v1.EngineSpec{
			Versions: []*v1.EngineVersion{{
				Version: "0.5.0",
				Images: map[string]*v1.EngineImage{
					"nvidia_gpu": {ImageName: "neutree/engine-vllm", Tag: "v0.5.0-ray2.53.0"},
				},
			}},
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
		cluster:        cluster,
		storage:        s,
		acceleratorMgr: acceleratorMgr,
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

			mockAcceleratorMgr := acceleratormocks.NewMockManager(t)
			mockAcceleratorMgr.EXPECT().GetEngineContainerRunOptions(mock.Anything).Return([]string{"--runtime=nvidia", "--gpus all"}, nil).Maybe()
			mockAcceleratorMgr.EXPECT().GetAllConverters().Return(map[string]plugin.ResourceConverter{}).Maybe()
			mockAcceleratorMgr.EXPECT().GetAllParsers().Return(map[string]plugin.ResourceParser{}).Maybe()

			o, ctx := newTestRayOrchestratorCtx(mockStorage, mockDashboard, endpoint, mockAcceleratorMgr)

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

			mockAcceleratorMgr := acceleratormocks.NewMockManager(t)
			mockAcceleratorMgr.EXPECT().GetEngineContainerRunOptions(mock.Anything).Return([]string{"--runtime=nvidia", "--gpus all"}, nil).Maybe()
			mockAcceleratorMgr.EXPECT().GetAllConverters().Return(map[string]plugin.ResourceConverter{}).Maybe()
			mockAcceleratorMgr.EXPECT().GetAllParsers().Return(map[string]plugin.ResourceParser{}).Maybe()

			o, ctx := newTestRayOrchestratorCtx(mockStorage, mockDashboard, endpoint, mockAcceleratorMgr)

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

			mockAcceleratorMgr := acceleratormocks.NewMockManager(t)
			mockAcceleratorMgr.EXPECT().GetEngineContainerRunOptions(mock.Anything).Return([]string{"--runtime=nvidia", "--gpus all"}, nil).Maybe()
			mockAcceleratorMgr.EXPECT().GetAllConverters().Return(map[string]plugin.ResourceConverter{}).Maybe()
			mockAcceleratorMgr.EXPECT().GetAllParsers().Return(map[string]plugin.ResourceParser{}).Maybe()

			o, ctx := newTestRayOrchestratorCtx(mockStorage, mockDashboard, endpoint, mockAcceleratorMgr)

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

	app, err := EndpointToApplication(endpoint, deployedCluster, modelRegistry, nil, nil, mgr)
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

	app, err := EndpointToApplication(endpoint, deployedCluster, modelRegistry, nil, nil, mgr)
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

	app, err := EndpointToApplication(endpoint, &v1.Cluster{}, modelRegistry, nil, nil, nil)
	assert.NoError(t, err)

	// Verify that the route prefix includes workspace
	assert.Equal(t, "/production/chat-model", app.RoutePrefix)
}

func TestEndpointToApplication_ImportPathStripsVariantSuffix(t *testing.T) {
	tests := []struct {
		name               string
		engineVersion      string
		expectedImportPath string
	}{
		{
			name:               "plain version",
			engineVersion:      "v0.11.2",
			expectedImportPath: "serve.vllm.v0_11_2.app:app_builder",
		},
		{
			name:               "cuda variant stripped",
			engineVersion:      "v0.17.1-cu130",
			expectedImportPath: "serve.vllm.v0_17_1.app:app_builder",
		},
		{
			name:               "rocm variant stripped",
			engineVersion:      "v0.17.1-rocm60",
			expectedImportPath: "serve.vllm.v0_17_1.app:app_builder",
		},
		{
			name:               "v0.18.1 plain version",
			engineVersion:      "v0.18.1",
			expectedImportPath: "serve.vllm.v0_18_1.app:app_builder",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			endpoint := &v1.Endpoint{
				Metadata: &v1.Metadata{
					Workspace: "ws",
					Name:      "ep",
				},
				Spec: &v1.EndpointSpec{
					Engine: &v1.EndpointEngineSpec{
						Engine:  "vllm",
						Version: tt.engineVersion,
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

			app, err := EndpointToApplication(endpoint, &v1.Cluster{}, modelRegistry, nil, nil, nil)
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedImportPath, app.ImportPath)
		})
	}
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

	app, err := EndpointToApplication(endpoint, cluster, modelRegistry, nil, nil, nil)
	assert.NoError(t, err)

	deploymentOptions := app.Args["deployment_options"].(map[string]interface{})
	scheduler := deploymentOptions["scheduler"].(map[string]interface{})
	assert.Equal(t, "pow2", scheduler["type"])
}

func TestEndpointToApplication_ResourceNameNormalization(t *testing.T) {
	makeEndpoint := func(product string) *v1.Endpoint {
		gpu := "2"
		ep := &v1.Endpoint{
			Metadata: &v1.Metadata{
				Name:      "ep",
				Workspace: "ws",
			},
			Spec: &v1.EndpointSpec{
				Engine: &v1.EndpointEngineSpec{
					Engine:  "vllm",
					Version: "v0.11.2",
				},
				Model: &v1.ModelSpec{
					Name:    "m",
					Version: "v1",
					Task:    "text-generation",
				},
				Resources: &v1.ResourceSpec{
					GPU: &gpu,
				},
				Replicas:          v1.ReplicaSpec{Num: intPtr(1)},
				DeploymentOptions: map[string]interface{}{},
				Env:               map[string]string{},
			},
		}
		ep.Spec.Resources.SetAcceleratorType(string(v1.AcceleratorTypeNVIDIAGPU))
		ep.Spec.Resources.SetAcceleratorProduct(product)
		return ep
	}

	modelRegistry := &v1.ModelRegistry{
		Spec: &v1.ModelRegistrySpec{
			Type: v1.BentoMLModelRegistryType,
		},
	}

	nvidiaGPU := string(v1.AcceleratorTypeNVIDIAGPU)

	engine := &v1.Engine{
		Metadata: &v1.Metadata{Name: "vllm"},
		Spec: &v1.EngineSpec{
			Versions: []*v1.EngineVersion{
				{
					Version: "v0.11.2",
					Images: map[string]*v1.EngineImage{
						"nvidia_gpu": {ImageName: "neutree/engine-vllm", Tag: "v0.11.2-ray2.53.0"},
					},
				},
			},
		},
	}

	tests := []struct {
		name           string
		product        string
		clusterVersion string
		expectedResKey string
	}{
		{
			name:           "v1.0.1 preserves underscored resource name",
			product:        "NVIDIA_L20",
			clusterVersion: "v1.0.1",
			expectedResKey: "NVIDIA_L20",
		},
		{
			name:           "v1.0.0 preserves underscored resource name",
			product:        "NVIDIA_L20",
			clusterVersion: "v1.0.0",
			expectedResKey: "NVIDIA_L20",
		},
		{
			name:           "v1.0.1 no-op for name without underscore",
			product:        "NVIDIAL20",
			clusterVersion: "v1.0.1",
			expectedResKey: "NVIDIAL20",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := acceleratormocks.NewMockManager(t)
			mgr.EXPECT().GetConverter(nvidiaGPU).
				Return(plugin.NewGPUConverter(), true)
			mgr.EXPECT().GetEngineContainerRunOptions(nvidiaGPU).
				Return([]string{"--runtime=nvidia", "--gpus all"}, nil).Maybe()

			cluster := &v1.Cluster{
				Spec: &v1.ClusterSpec{
					Version: tt.clusterVersion,
				},
				Status: &v1.ClusterStatus{
					AcceleratorType: &nvidiaGPU,
				},
			}

			app, err := EndpointToApplication(makeEndpoint(tt.product), cluster, modelRegistry, engine, nil, mgr)
			assert.NoError(t, err)

			deploymentOptions := app.Args["deployment_options"].(map[string]interface{})
			backend := deploymentOptions["backend"].(map[string]interface{})
			resources := backend["resources"].(map[string]float64)

			_, exists := resources[tt.expectedResKey]
			assert.True(t, exists, "expected resource key %q in resources: %v", tt.expectedResKey, resources)
		})
	}
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
			expectedEnvs: map[string]string{
				"ENGINE_NAME": "", "ENGINE_VERSION": "",
			},
			wantErr: false,
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
			expectedEnvs: map[string]string{
				"ENGINE_NAME": "", "ENGINE_VERSION": "",
			},
			wantErr: false,
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
			expectedEnvs: map[string]string{
				"ENGINE_NAME": "", "ENGINE_VERSION": "",
			},
			wantErr: false,
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
				v1.HFEndpoint:    "https://huggingface.co",
				v1.HFTokenEnv:    "test-token",
				"ENGINE_NAME": "", "ENGINE_VERSION": "",
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
				v1.HFEndpoint:    "https://huggingface.co",
				v1.HFTokenEnv:    "test-token",
				"ENGINE_NAME": "", "ENGINE_VERSION": "",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, err := EndpointToApplication(tt.endpoint, tt.cluster, tt.modelRegistry, nil, nil, nil)
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

func TestBuildEngineContainerConfigs(t *testing.T) {
	defaultImageRegistry := &v1.ImageRegistry{
		Spec: &v1.ImageRegistrySpec{
			URL: "http://registry.example.com",
		},
	}

	nvidiaGPU := string(v1.AcceleratorTypeNVIDIAGPU)

	defaultEngine := &v1.Engine{
		Metadata: &v1.Metadata{Name: "vllm"},
		Spec: &v1.EngineSpec{
			Versions: []*v1.EngineVersion{
				{
					Version: "v0.12.0",
					Images: map[string]*v1.EngineImage{
						"nvidia_gpu": {
							ImageName: "neutree/engine-vllm",
							Tag:       "v0.12.0-ray2.53.0",
						},
					},
				},
			},
		},
	}

	cpuEngine := &v1.Engine{
		Metadata: &v1.Metadata{Name: "llama-cpp"},
		Spec: &v1.EngineSpec{
			Versions: []*v1.EngineVersion{
				{
					Version: "v0.3.7",
					Images: map[string]*v1.EngineImage{
						"cpu": {ImageName: "neutree/engine-llama-cpp", Tag: "v0.3.7-ray2.53.0"},
					},
				},
			},
		},
	}

	gpuEndpoint := func(engineVersion string) *v1.Endpoint {
		return &v1.Endpoint{
			Spec: &v1.EndpointSpec{
				Engine: &v1.EndpointEngineSpec{Engine: "vllm", Version: engineVersion},
				Resources: &v1.ResourceSpec{
					Accelerator: map[string]string{
						v1.AcceleratorTypeKey: nvidiaGPU,
					},
				},
			},
		}
	}

	cpuEndpoint := &v1.Endpoint{
		Spec: &v1.EndpointSpec{
			Engine:    &v1.EndpointEngineSpec{Engine: "llama-cpp", Version: "v0.3.7"},
			Resources: &v1.ResourceSpec{},
		},
	}

	tests := []struct {
		name                   string
		endpoint               *v1.Endpoint
		engine                 *v1.Engine
		imageRegistry          *v1.ImageRegistry
		modelCaches            []v1.ModelCache
		modelRegistry          *v1.ModelRegistry
		setupMgr               func(t *testing.T) *acceleratormocks.MockManager
		setupModelRegistry     func(t *testing.T)
		expectErr              bool
		expectErrMsg           string
		expectedImage          string
		expectedBaseOptions    []string
		expectedBackendOptions []string
	}{
		{
			name:          "GPU endpoint with registry generates split container configs",
			endpoint:      gpuEndpoint("v0.12.0"),
			engine:        defaultEngine,
			imageRegistry: defaultImageRegistry,
			expectedImage: "registry.example.com/neutree/engine-vllm:v0.12.0-ray2.53.0",
			expectedBaseOptions: []string{
				"--rm",
			},
			expectedBackendOptions: []string{
				"--runtime=nvidia",
				"--gpus all",
				"--rm",
			},
		},
		{
			name:          "model caches included only in backend config",
			endpoint:      gpuEndpoint("v0.12.0"),
			engine:        defaultEngine,
			imageRegistry: defaultImageRegistry,
			modelCaches: []v1.ModelCache{
				{
					Name:     "default",
					HostPath: &corev1.HostPathVolumeSource{Path: "/data/models"},
				},
			},
			expectedImage: "registry.example.com/neutree/engine-vllm:v0.12.0-ray2.53.0",
			expectedBaseOptions: []string{
				"--rm",
			},
			expectedBackendOptions: []string{
				"--runtime=nvidia",
				"--gpus all",
				"-v /data/models:" + filepath.Join(v1.DefaultSSHClusterModelCacheMountPath, "default"),
				"--rm",
			},
		},
		{
			name:          "model cache without HostPath is skipped",
			endpoint:      gpuEndpoint("v0.12.0"),
			engine:        defaultEngine,
			imageRegistry: defaultImageRegistry,
			modelCaches: []v1.ModelCache{
				{Name: "nfs-cache"},
			},
			expectedImage: "registry.example.com/neutree/engine-vllm:v0.12.0-ray2.53.0",
			expectedBaseOptions: []string{
				"--rm",
			},
			expectedBackendOptions: []string{
				"--runtime=nvidia",
				"--gpus all",
				"--rm",
			},
		},
		{
			name:     "registry with custom repository",
			endpoint: gpuEndpoint("v0.12.0"),
			engine:   defaultEngine,
			imageRegistry: &v1.ImageRegistry{
				Spec: &v1.ImageRegistrySpec{
					URL:        "http://registry.example.com",
					Repository: "custom-repo",
				},
			},
			expectedImage: "registry.example.com/custom-repo/neutree/engine-vllm:v0.12.0-ray2.53.0",
			expectedBaseOptions: []string{
				"--rm",
			},
			expectedBackendOptions: []string{
				"--runtime=nvidia",
				"--gpus all",
				"--rm",
			},
		},
		{
			name:          "CPU endpoint produces only --rm in backend",
			endpoint:      cpuEndpoint,
			engine:        cpuEngine,
			expectedImage: "neutree/engine-llama-cpp:v0.3.7-ray2.53.0",
			expectedBaseOptions: []string{
				"--rm",
			},
			expectedBackendOptions: []string{
				"--rm",
			},
		},
		{
			name:          "nil imageRegistry omits registry prefix",
			endpoint:      gpuEndpoint("v0.12.0"),
			engine:        defaultEngine,
			imageRegistry: nil,
			expectedImage: "neutree/engine-vllm:v0.12.0-ray2.53.0",
			expectedBaseOptions: []string{
				"--rm",
			},
			expectedBackendOptions: []string{
				"--runtime=nvidia",
				"--gpus all",
				"--rm",
			},
		},
		{
			name:     "multiple model caches with mixed HostPath",
			endpoint: cpuEndpoint,
			engine:   cpuEngine,
			modelCaches: []v1.ModelCache{
				{Name: "cache-1", HostPath: &corev1.HostPathVolumeSource{Path: "/data/cache1"}},
				{Name: "cache-2", HostPath: &corev1.HostPathVolumeSource{Path: "/data/cache2"}},
				{Name: "nfs-cache"}, // No HostPath, should be skipped
			},
			expectedImage: "neutree/engine-llama-cpp:v0.3.7-ray2.53.0",
			expectedBaseOptions: []string{
				"--rm",
			},
			expectedBackendOptions: []string{
				"-v /data/cache1:" + filepath.Join(v1.DefaultSSHClusterModelCacheMountPath, "cache-1"),
				"-v /data/cache2:" + filepath.Join(v1.DefaultSSHClusterModelCacheMountPath, "cache-2"),
				"--rm",
			},
		},
		// NFS mount cases
		{
			name: "NFS nfs4 type uses type=nfs with nfsvers=4",
			endpoint: &v1.Endpoint{
				Metadata: &v1.Metadata{Workspace: "ws", Name: "ep"},
				Spec: &v1.EndpointSpec{
					Engine:    &v1.EndpointEngineSpec{Engine: "vllm", Version: "v0.12.0"},
					Resources: &v1.ResourceSpec{Accelerator: map[string]string{v1.AcceleratorTypeKey: nvidiaGPU}},
				},
			},
			engine:        defaultEngine,
			imageRegistry: defaultImageRegistry,
			modelRegistry: &v1.ModelRegistry{
				Spec: &v1.ModelRegistrySpec{
					Type: v1.BentoMLModelRegistryType,
					Url:  "nfs://10.0.0.1:/models",
				},
			},
			setupMgr: func(t *testing.T) *acceleratormocks.MockManager {
				mgr := acceleratormocks.NewMockManager(t)
				mgr.EXPECT().GetEngineContainerRunOptions(nvidiaGPU).Return([]string{"--runtime=nvidia", "--gpus all"}, nil)
				return mgr
			},
			setupModelRegistry: func(t *testing.T) {
				mockRegistry := modelregistrymocks.NewMockModelRegistry(t)
				mockRegistry.EXPECT().GetNFSVersion().Return("4.1", nil)
				model_registry.NewModelRegistry = func(_ *v1.ModelRegistry) (model_registry.ModelRegistry, error) {
					return mockRegistry, nil
				}
			},
			expectedImage: "registry.example.com/neutree/engine-vllm:v0.12.0-ray2.53.0",
			expectedBaseOptions: []string{
				"--rm",
			},
			expectedBackendOptions: []string{
				"--runtime=nvidia",
				"--gpus all",
				`--mount 'type=volume,dst=/mnt/ws/ep,volume-opt=type=nfs,"volume-opt=o=addr=10.0.0.1,nfsvers=4.1",volume-opt=device=:/models'`,
				"--rm",
			},
		},
		{
			name: "NFS v3 uses type=nfs without nfsvers",
			endpoint: &v1.Endpoint{
				Metadata: &v1.Metadata{Workspace: "ws", Name: "ep"},
				Spec: &v1.EndpointSpec{
					Engine:    &v1.EndpointEngineSpec{Engine: "vllm", Version: "v0.12.0"},
					Resources: &v1.ResourceSpec{Accelerator: map[string]string{v1.AcceleratorTypeKey: nvidiaGPU}},
				},
			},
			engine:        defaultEngine,
			imageRegistry: defaultImageRegistry,
			modelRegistry: &v1.ModelRegistry{
				Spec: &v1.ModelRegistrySpec{
					Type: v1.BentoMLModelRegistryType,
					Url:  "nfs://10.0.0.1:/models",
				},
			},
			setupMgr: func(t *testing.T) *acceleratormocks.MockManager {
				mgr := acceleratormocks.NewMockManager(t)
				mgr.EXPECT().GetEngineContainerRunOptions(nvidiaGPU).Return([]string{"--runtime=nvidia", "--gpus all"}, nil)
				return mgr
			},
			setupModelRegistry: func(t *testing.T) {
				mockRegistry := modelregistrymocks.NewMockModelRegistry(t)
				mockRegistry.EXPECT().GetNFSVersion().Return("3", nil)
				model_registry.NewModelRegistry = func(_ *v1.ModelRegistry) (model_registry.ModelRegistry, error) {
					return mockRegistry, nil
				}
			},
			expectedImage: "registry.example.com/neutree/engine-vllm:v0.12.0-ray2.53.0",
			expectedBaseOptions: []string{
				"--rm",
			},
			expectedBackendOptions: []string{
				"--runtime=nvidia",
				"--gpus all",
				"--mount 'type=volume,dst=/mnt/ws/ep,volume-opt=type=nfs,volume-opt=o=addr=10.0.0.1,volume-opt=device=:/models'",
				"--rm",
			},
		},
		// Error cases
		{
			name:         "nil endpoint",
			endpoint:     nil,
			engine:       &v1.Engine{Metadata: &v1.Metadata{Name: "vllm"}, Spec: &v1.EngineSpec{}},
			expectErr:    true,
			expectErrMsg: "endpoint with engine spec is required",
		},
		{
			name: "endpoint without engine spec",
			endpoint: &v1.Endpoint{
				Spec: &v1.EndpointSpec{},
			},
			engine:       &v1.Engine{Metadata: &v1.Metadata{Name: "vllm"}, Spec: &v1.EngineSpec{}},
			expectErr:    true,
			expectErrMsg: "endpoint with engine spec is required",
		},
		{
			name: "nil engine",
			endpoint: &v1.Endpoint{
				Spec: &v1.EndpointSpec{
					Engine: &v1.EndpointEngineSpec{Engine: "vllm", Version: "v0.12.0"},
				},
			},
			engine:       nil,
			expectErr:    true,
			expectErrMsg: "engine is required",
		},
		{
			name: "version not found in engine",
			endpoint: &v1.Endpoint{
				Spec: &v1.EndpointSpec{
					Engine:    &v1.EndpointEngineSpec{Engine: "vllm", Version: "v99.0.0"},
					Resources: &v1.ResourceSpec{},
				},
			},
			engine: &v1.Engine{
				Metadata: &v1.Metadata{Name: "vllm"},
				Spec: &v1.EngineSpec{
					Versions: []*v1.EngineVersion{
						{Version: "v0.12.0"},
					},
				},
			},
			expectErr:    true,
			expectErrMsg: "engine version v99.0.0 not found",
		},
		{
			name:     "engine version without image returns error",
			endpoint: gpuEndpoint("v0.8.5"),
			engine: &v1.Engine{
				Metadata: &v1.Metadata{Name: "vllm"},
				Spec: &v1.EngineSpec{
					Versions: []*v1.EngineVersion{
						{Version: "v0.8.5"},
					},
				},
			},
			imageRegistry: defaultImageRegistry,
			expectErr:     true,
			expectErrMsg:  "no engine image configured for accelerator",
		},
		{
			name:     "SSH image key takes priority over generic key",
			endpoint: gpuEndpoint("v0.11.2"),
			engine: &v1.Engine{
				Metadata: &v1.Metadata{Name: "vllm"},
				Spec: &v1.EngineSpec{
					Versions: []*v1.EngineVersion{
						{
							Version: "v0.11.2",
							Images: map[string]*v1.EngineImage{
								"nvidia_gpu": {
									ImageName: "vllm/vllm-openai",
									Tag:       "v0.11.2",
								},
								v1.SSHImageKeyPrefix + "nvidia_gpu": {
									ImageName: "neutree/engine-vllm",
									Tag:       "v0.11.2-ray2.53.0",
								},
							},
						},
					},
				},
			},
			imageRegistry: defaultImageRegistry,
			expectedImage: "registry.example.com/neutree/engine-vllm:v0.11.2-ray2.53.0",
			expectedBaseOptions: []string{
				"--rm",
			},
			expectedBackendOptions: []string{
				"--runtime=nvidia",
				"--gpus all",
				"--rm",
			},
		},
		{
			name:     "SSH CPU image key takes priority over generic CPU key",
			endpoint: cpuEndpoint,
			engine: &v1.Engine{
				Metadata: &v1.Metadata{Name: "llama-cpp"},
				Spec: &v1.EngineSpec{
					Versions: []*v1.EngineVersion{
						{
							Version: "v0.3.7",
							Images: map[string]*v1.EngineImage{
								"cpu": {
									ImageName: "neutree/llama-cpp-python",
									Tag:       "v0.3.7",
								},
								v1.SSHImageKeyPrefix + "cpu": {
									ImageName: "neutree/engine-llama-cpp",
									Tag:       "v0.3.7-ray2.53.0",
								},
							},
						},
					},
				},
			},
			imageRegistry: defaultImageRegistry,
			expectedImage: "registry.example.com/neutree/engine-llama-cpp:v0.3.7-ray2.53.0",
			expectedBaseOptions: []string{
				"--rm",
			},
			expectedBackendOptions: []string{
				"--rm",
			},
		},
		{
			name:     "falls back to generic key when SSH key missing",
			endpoint: gpuEndpoint("v0.12.0"),
			engine:   defaultEngine,
			imageRegistry: defaultImageRegistry,
			expectedImage: "registry.example.com/neutree/engine-vllm:v0.12.0-ray2.53.0",
			expectedBaseOptions: []string{
				"--rm",
			},
			expectedBackendOptions: []string{
				"--runtime=nvidia",
				"--gpus all",
				"--rm",
			},
		},
		{
			name:     "accelerator manager error propagates",
			endpoint: gpuEndpoint("v0.12.0"),
			engine:   defaultEngine,
			setupMgr: func(t *testing.T) *acceleratormocks.MockManager {
				mgr := acceleratormocks.NewMockManager(t)
				mgr.EXPECT().GetEngineContainerRunOptions(nvidiaGPU).Return(nil, assert.AnError)
				return mgr
			},
			expectErr:    true,
			expectErrMsg: "failed to get engine container run options",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setupModelRegistry != nil {
				origNewModelRegistry := model_registry.NewModelRegistry
				tt.setupModelRegistry(t)
				defer func() { model_registry.NewModelRegistry = origNewModelRegistry }()
			}

			var mgr *acceleratormocks.MockManager
			if tt.setupMgr != nil {
				mgr = tt.setupMgr(t)
			} else {
				mgr = acceleratormocks.NewMockManager(t)
				mgr.EXPECT().GetEngineContainerRunOptions(nvidiaGPU).Return([]string{"--runtime=nvidia", "--gpus all"}, nil).Maybe()
			}

			baseConfig, backendConfig, err := buildEngineContainerConfigs(tt.endpoint, tt.engine, tt.imageRegistry, mgr, tt.modelCaches, tt.modelRegistry)

			if tt.expectErr {
				assert.Error(t, err)
				if tt.expectErrMsg != "" {
					assert.Contains(t, err.Error(), tt.expectErrMsg)
				}
				assert.Nil(t, baseConfig)
				assert.Nil(t, backendConfig)
				return
			}

			assert.NoError(t, err)
			assert.NotNil(t, baseConfig)
			assert.NotNil(t, backendConfig)

			assert.Equal(t, tt.expectedImage, baseConfig["image"])
			baseOptions, ok := baseConfig["run_options"].([]string)
			assert.True(t, ok)
			assert.Equal(t, tt.expectedBaseOptions, baseOptions)

			assert.Equal(t, tt.expectedImage, backendConfig["image"])
			backendOptions, ok := backendConfig["run_options"].([]string)
			assert.True(t, ok)
			assert.Equal(t, tt.expectedBackendOptions, backendOptions)
		})
	}
}

func TestEndpointToApplication_ContainerConfig(t *testing.T) {
	nvidiaGPU := string(v1.AcceleratorTypeNVIDIAGPU)

	hfModelRegistry := &v1.ModelRegistry{
		Spec: &v1.ModelRegistrySpec{
			Type: v1.HuggingFaceModelRegistryType,
			Url:  "https://huggingface.co",
		},
	}

	defaultImageRegistry := &v1.ImageRegistry{
		Spec: &v1.ImageRegistrySpec{
			URL: "http://registry.example.com",
		},
	}

	vllmEngine := &v1.Engine{
		Metadata: &v1.Metadata{Name: "vllm"},
		Spec: &v1.EngineSpec{
			Versions: []*v1.EngineVersion{
				{
					Version: "v0.12.0",
					Images: map[string]*v1.EngineImage{
						"nvidia_gpu": {ImageName: "neutree/engine-vllm", Tag: "v0.12.0-ray2.53.0"},
					},
				},
			},
		},
	}

	cpuEngine := &v1.Engine{
		Metadata: &v1.Metadata{Name: "llama-cpp"},
		Spec: &v1.EngineSpec{
			Versions: []*v1.EngineVersion{
				{
					Version: "v0.3.7",
					Images: map[string]*v1.EngineImage{
						"cpu": {ImageName: "neutree/engine-llama-cpp", Tag: "v0.3.7-ray2.53.0"},
					},
				},
			},
		},
	}

	tests := []struct {
		name                       string
		endpoint                   *v1.Endpoint
		cluster                    *v1.Cluster
		engine                     *v1.Engine
		imageRegistry              *v1.ImageRegistry
		setupMgr                   func(t *testing.T) *acceleratormocks.MockManager
		expectContainer            bool
		expectedContainerImage     string
		expectedBaseRunOptions     []string
		expectedBackendRunOptions  []string
		expectedEngineName         string
		expectedEngineVersion      string
	}{
		{
			name: "new GPU cluster generates container and backend_container",
			endpoint: &v1.Endpoint{
				Metadata: &v1.Metadata{Workspace: "default", Name: "test-ep"},
				Spec: &v1.EndpointSpec{
					Engine:  &v1.EndpointEngineSpec{Engine: "vllm", Version: "v0.12.0"},
					Model:   &v1.ModelSpec{Name: "test-model", Task: v1.TextGenerationModelTask},
					Resources: &v1.ResourceSpec{
						Accelerator: map[string]string{v1.AcceleratorTypeKey: nvidiaGPU},
					},
					DeploymentOptions: map[string]interface{}{},
				},
			},
			cluster: &v1.Cluster{
				Spec: &v1.ClusterSpec{
					Version: "v1.0.1",
					Config: &v1.ClusterConfig{
						ModelCaches: []v1.ModelCache{
							{Name: "default", HostPath: &corev1.HostPathVolumeSource{Path: "/data/models"}},
						},
					},
				},
				Status: &v1.ClusterStatus{AcceleratorType: &nvidiaGPU},
			},
			engine:        vllmEngine,
			imageRegistry: defaultImageRegistry,
			setupMgr: func(t *testing.T) *acceleratormocks.MockManager {
				mgr := acceleratormocks.NewMockManager(t)
				mgr.EXPECT().GetConverter(nvidiaGPU).Return(plugin.NewGPUConverter(), true)
				mgr.EXPECT().GetEngineContainerRunOptions(nvidiaGPU).Return([]string{"--runtime=nvidia", "--gpus all"}, nil)
				return mgr
			},
			expectContainer:        true,
			expectedContainerImage: "registry.example.com/neutree/engine-vllm:v0.12.0-ray2.53.0",
			expectedBaseRunOptions: []string{"--rm"},
			expectedBackendRunOptions: []string{
				"--runtime=nvidia", "--gpus all",
				"-v /data/models:" + filepath.Join(v1.DefaultSSHClusterModelCacheMountPath, "default"),
				"--rm",
			},
			expectedEngineName:    "vllm",
			expectedEngineVersion: "v0.12.0",
		},
		{
			name: "old cluster (v1.0.0) does not generate container config",
			endpoint: &v1.Endpoint{
				Metadata: &v1.Metadata{Workspace: "default", Name: "test-ep"},
				Spec: &v1.EndpointSpec{
					Engine:            &v1.EndpointEngineSpec{Engine: "vllm", Version: "v0.12.0"},
					Model:             &v1.ModelSpec{Name: "test-model", Task: v1.TextGenerationModelTask},
					Resources:         &v1.ResourceSpec{Accelerator: map[string]string{v1.AcceleratorTypeKey: nvidiaGPU}},
					DeploymentOptions: map[string]interface{}{},
				},
			},
			cluster: &v1.Cluster{
				Spec:   &v1.ClusterSpec{Version: "v1.0.0"},
				Status: &v1.ClusterStatus{AcceleratorType: &nvidiaGPU},
			},
			engine: vllmEngine,
			setupMgr: func(t *testing.T) *acceleratormocks.MockManager {
				mgr := acceleratormocks.NewMockManager(t)
				mgr.EXPECT().GetConverter(nvidiaGPU).Return(plugin.NewGPUConverter(), true)
				return mgr
			},
			expectContainer:       false,
			expectedEngineName:    "vllm",
			expectedEngineVersion: "v0.12.0",
		},
		{
			name: "empty cluster version treated as old cluster",
			endpoint: &v1.Endpoint{
				Metadata: &v1.Metadata{Workspace: "default", Name: "test-ep"},
				Spec: &v1.EndpointSpec{
					Engine:            &v1.EndpointEngineSpec{Engine: "vllm", Version: "v0.12.0"},
					Model:             &v1.ModelSpec{Name: "test-model", Task: v1.TextGenerationModelTask},
					Resources:         &v1.ResourceSpec{},
					DeploymentOptions: map[string]interface{}{},
				},
			},
			cluster:               &v1.Cluster{},
			expectContainer:       false,
			expectedEngineName:    "vllm",
			expectedEngineVersion: "v0.12.0",
		},
		{
			name: "CPU endpoint on new cluster generates container without GPU options",
			endpoint: &v1.Endpoint{
				Metadata: &v1.Metadata{Workspace: "default", Name: "cpu-ep"},
				Spec: &v1.EndpointSpec{
					Engine:            &v1.EndpointEngineSpec{Engine: "llama-cpp", Version: "v0.3.7"},
					Model:             &v1.ModelSpec{Name: "test-model", Task: v1.TextGenerationModelTask},
					Resources:         &v1.ResourceSpec{},
					DeploymentOptions: map[string]interface{}{},
				},
			},
			cluster: &v1.Cluster{
				Spec: &v1.ClusterSpec{Version: "v1.0.1"},
			},
			engine:                 cpuEngine,
			imageRegistry:          defaultImageRegistry,
			expectContainer:        true,
			expectedContainerImage: "registry.example.com/neutree/engine-llama-cpp:v0.3.7-ray2.53.0",
			expectedBaseRunOptions: []string{"--rm"},
			expectedBackendRunOptions: []string{"--rm"},
			expectedEngineName:     "llama-cpp",
			expectedEngineVersion:  "v0.3.7",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mgr *acceleratormocks.MockManager
			if tt.setupMgr != nil {
				mgr = tt.setupMgr(t)
			}

			app, err := EndpointToApplication(tt.endpoint, tt.cluster, hfModelRegistry, tt.engine, tt.imageRegistry, mgr)
			assert.NoError(t, err)

			// Verify ENGINE_NAME / ENGINE_VERSION env vars
			envs := app.RuntimeEnv["env_vars"].(map[string]string)
			assert.Equal(t, tt.expectedEngineName, envs["ENGINE_NAME"])
			assert.Equal(t, tt.expectedEngineVersion, envs["ENGINE_VERSION"])

			if !tt.expectContainer {
				_, hasContainer := app.RuntimeEnv["container"]
				assert.False(t, hasContainer, "should not have runtime_env.container")
				_, hasBackend := app.Args["backend_container"]
				assert.False(t, hasBackend, "should not have backend_container")
				return
			}

			// Verify base container config
			container, ok := app.RuntimeEnv["container"].(map[string]interface{})
			assert.True(t, ok, "runtime_env should have 'container'")
			assert.Equal(t, tt.expectedContainerImage, container["image"])

			baseOpts, ok := container["run_options"].([]string)
			assert.True(t, ok)
			assert.Equal(t, tt.expectedBaseRunOptions, baseOpts)

			// Verify backend container config
			backendContainer, ok := app.Args["backend_container"].(map[string]interface{})
			assert.True(t, ok, "args should have 'backend_container'")
			assert.Equal(t, tt.expectedContainerImage, backendContainer["image"])

			backendOpts, ok := backendContainer["run_options"].([]string)
			assert.True(t, ok)
			assert.Equal(t, tt.expectedBackendRunOptions, backendOpts)
		})
	}
}

func TestEndpointToApplication_VLLMTensorParallelSize(t *testing.T) {
	nvidiaGPU := string(v1.AcceleratorTypeNVIDIAGPU)

	modelRegistry := &v1.ModelRegistry{
		Spec: &v1.ModelRegistrySpec{
			Type: v1.HuggingFaceModelRegistryType,
		},
	}
	deployedCluster := &v1.Cluster{}

	makeEndpoint := func(gpu string, variables map[string]interface{}) *v1.Endpoint {
		ep := &v1.Endpoint{
			Metadata: &v1.Metadata{
				Workspace: "test",
				Name:      "test-endpoint",
			},
			Spec: &v1.EndpointSpec{
				Engine: &v1.EndpointEngineSpec{
					Engine:  "vllm",
					Version: "v0.11.2",
				},
				Resources: &v1.ResourceSpec{
					GPU: &gpu,
				},
				Replicas: v1.ReplicaSpec{
					Num: pointy.Int(1),
				},
				Variables: variables,
				Model: &v1.ModelSpec{
					Name: "test-model",
				},
			},
		}
		ep.Spec.Resources.SetAcceleratorType(nvidiaGPU)
		return ep
	}

	tests := []struct {
		name                    string
		gpu                     string
		variables               map[string]interface{}
		expectedTensorParallel  interface{}
		expectEngineArgsPresent bool
	}{
		{
			name:                    "GPU=4 should auto-set tensor_parallel_size=4",
			gpu:                     "4",
			variables:               nil,
			expectedTensorParallel:  4,
			expectEngineArgsPresent: true,
		},
		{
			name:                    "GPU=2 should auto-set tensor_parallel_size=2",
			gpu:                     "2",
			variables:               nil,
			expectedTensorParallel:  2,
			expectEngineArgsPresent: true,
		},
		{
			name:                    "GPU=1 should not set tensor_parallel_size",
			gpu:                     "1",
			variables:               nil,
			expectEngineArgsPresent: false,
		},
		{
			name:                    "fractional GPU should not set tensor_parallel_size",
			gpu:                     "2.5",
			variables:               nil,
			expectEngineArgsPresent: false,
		},
		{
			name: "user-provided tensor_parallel_size should not be overridden",
			gpu:  "4",
			variables: map[string]interface{}{
				"engine_args": map[string]interface{}{
					"tensor_parallel_size": 2,
				},
			},
			expectedTensorParallel:  2,
			expectEngineArgsPresent: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := acceleratormocks.NewMockManager(t)
			mgr.EXPECT().GetConverter(nvidiaGPU).
				Return(plugin.NewGPUConverter(), true)

			app, err := EndpointToApplication(makeEndpoint(tt.gpu, tt.variables), deployedCluster, modelRegistry, nil, nil, mgr)
			assert.NoError(t, err)

			engineArgs, hasEngineArgs := app.Args["engine_args"].(map[string]interface{})
			if tt.expectEngineArgsPresent {
				assert.True(t, hasEngineArgs, "engine_args should be present")
				assert.Equal(t, tt.expectedTensorParallel, engineArgs["tensor_parallel_size"])
			} else {
				if hasEngineArgs {
					_, hasTensorParallel := engineArgs["tensor_parallel_size"]
					assert.False(t, hasTensorParallel, "tensor_parallel_size should not be set")
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
			name: "return Failed when application is DEPLOYING but a deployment is UNHEALTHY",
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
							Deployments: map[string]dashboard.Deployment{
								"BACKEND": {
									Name:    "BACKEND",
									Status:  dashboard.DeploymentStatusUnhealthy,
									Message: "replica failed to start",
								},
							},
						},
					},
				}, nil)
			},
			expectedPhase:  v1.EndpointPhaseFAILED,
			expectErrorMsg: "Deployment BACKEND: replica failed to start",
			expectError:    false,
		},
		{
			name: "return Failed when application is NOT_STARTED but a deployment is UNHEALTHY",
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
							Deployments: map[string]dashboard.Deployment{
								"BACKEND": {
									Name:    "BACKEND",
									Status:  dashboard.DeploymentStatusUnhealthy,
									Message: "image pull failed",
								},
							},
						},
					},
				}, nil)
			},
			expectedPhase:  v1.EndpointPhaseFAILED,
			expectErrorMsg: "Deployment BACKEND: image pull failed",
			expectError:    false,
		},
		{
			name: "return Deploying when application is DEPLOYING and deployment is HEALTHY",
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
							Deployments: map[string]dashboard.Deployment{
								"BACKEND": {
									Name:   "BACKEND",
									Status: dashboard.DeploymentStatusHealthy,
								},
							},
						},
					},
				}, nil)
			},
			expectedPhase: v1.EndpointPhaseDEPLOYING,
			expectError:   false,
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
