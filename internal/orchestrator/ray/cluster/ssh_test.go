package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/neutree-ai/neutree/internal/orchestrator/ray/config"
	"github.com/neutree-ai/neutree/internal/orchestrator/ray/dashboard"
	dashboardmocks "github.com/neutree-ai/neutree/internal/orchestrator/ray/dashboard/mocks"
	registrymocks "github.com/neutree-ai/neutree/internal/registry/mocks"
	commandmocks "github.com/neutree-ai/neutree/pkg/command/mocks"
)

func TestSSHClusterManager_DownCluster(t *testing.T) {
	setUp(t)
	defer tearDown()

	tests := []struct {
		name           string
		workerIPs      []string
		mockExecOutput []byte
		mockExecError  error
		expectError    bool
	}{
		{
			name:           "success_with_no_workers",
			workerIPs:      []string{},
			mockExecOutput: []byte("success"),
			mockExecError:  nil,
			expectError:    false,
		},
		{
			name:           "success_with_workers",
			workerIPs:      []string{"1.1.1.1", "2.2.2.2"},
			mockExecOutput: []byte("success"),
			mockExecError:  nil,
			expectError:    false,
		},
		{
			name:           "failure_on_exec_error",
			workerIPs:      []string{},
			mockExecOutput: []byte("error"),
			mockExecError:  errors.New("exec error"),
			expectError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup
			mockExec := new(commandmocks.MockExecutor)
			mockExec.On("Execute", mock.Anything, "ray", mock.Anything).Return(tt.mockExecOutput, tt.mockExecError)

			clusterConfig := &v1.RayClusterConfig{
				ClusterName: "test-cluster",
				Provider: v1.Provider{
					WorkerIPs: tt.workerIPs,
				},
				Auth: v1.Auth{
					SSHUser:       "user",
					SSHPrivateKey: "dGVzdAo=",
				},
			}

			manager := &sshClusterManager{
				executor:  mockExec,
				configMgr: config.NewManager(clusterConfig.ClusterName),
				config:    clusterConfig,
			}

			// Test
			err := manager.DownCluster(context.Background())

			// Verify
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			mockExec.AssertExpectations(t)
		})
	}
}

func TestSSHClusterManager_UpCluster(t *testing.T) {
	setUp(t)
	defer tearDown()

	tests := []struct {
		name           string
		restart        bool
		mockExecOutput []byte
		mockExecError  error
		headIP         string
		expectError    bool
	}{
		{
			name:           "success_with_restart",
			restart:        true,
			mockExecOutput: []byte("success"),
			mockExecError:  nil,
			headIP:         "1.1.1.1",
			expectError:    false,
		},
		{
			name:           "success_without_restart",
			restart:        false,
			mockExecOutput: []byte("success"),
			mockExecError:  nil,
			headIP:         "2.2.2.2",
			expectError:    false,
		},
		{
			name:           "failure_on_exec_error",
			restart:        true,
			mockExecOutput: []byte("error"),
			mockExecError:  errors.New("exec error"),
			headIP:         "",
			expectError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup
			mockExec := new(commandmocks.MockExecutor)
			if tt.mockExecError != nil {
				mockExec.On("Execute", mock.Anything, "ray", mock.Anything).Return(tt.mockExecOutput, tt.mockExecError).Once()
			} else {
				mockExec.On("Execute", mock.Anything, "ray", mock.Anything).Return(tt.mockExecOutput, tt.mockExecError).Once()
			}

			clusterConfig := &v1.RayClusterConfig{
				ClusterName: "test-cluster",
				Auth: v1.Auth{
					SSHUser:       "user",
					SSHPrivateKey: "dGVzdAo=",
				},
				Provider: v1.Provider{
					HeadIP: tt.headIP,
				},
			}

			mockImageRegistryService := &registrymocks.MockImageService{}
			mockImageRegistryService.On("CheckImageExists", mock.Anything, mock.Anything).Return(true, nil)
			manager := &sshClusterManager{
				executor:  mockExec,
				configMgr: config.NewManager(clusterConfig.ClusterName),
				config:    clusterConfig,
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
					Status: &v1.ImageRegistryStatus{
						Phase: v1.ImageRegistryPhaseCONNECTED,
					},
				},
				imageService: mockImageRegistryService,
			}

			// Test
			ip, err := manager.UpCluster(context.Background(), tt.restart)

			// Verify
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.headIP, ip)
			}

			mockExec.AssertExpectations(t)
		})
	}
}

func TestStartNode_Success(t *testing.T) {
	setUp(t)
	defer tearDown()

	mockExec := new(commandmocks.MockExecutor)
	// init command
	mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Run(func(args mock.Arguments) {
		cmd := args.Get(2).([]string)
		assert.Contains(t, strings.Join(cmd, " "), "cmd1")
	}).Return([]byte("success"), nil).Once()
	mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Run(func(args mock.Arguments) {
		cmd := args.Get(2).([]string)
		assert.Contains(t, strings.Join(cmd, " "), "cmd2")
	}).Return([]byte("success"), nil).Once()
	// check docker installed
	mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Return([]byte("docker"), nil).Once()
	// check image
	mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Return([]byte(""), nil).Once()
	// check container status
	mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Return([]byte("true"), nil).Once()
	// ray start
	mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Run(func(args mock.Arguments) {
		cmd := args.Get(2).([]string)
		assert.Contains(t, strings.Join(cmd, " "), "ray start")
	}).Return([]byte(""), nil).Once()

	clusterConfig := &v1.RayClusterConfig{
		ClusterName: "test-cluster",
		Auth: v1.Auth{
			SSHUser:       "user",
			SSHPrivateKey: "dGVzdAo=",
		},
		Docker: v1.Docker{
			ContainerName: "test-container",
		},
		InitializationCommands:       []string{"cmd1", "cmd2"},
		StaticWorkerStartRayCommands: []string{"ray start"},
	}

	mockImageRegistryService := &registrymocks.MockImageService{}
	mockImageRegistryService.On("CheckImageExists", mock.Anything, mock.Anything).Return(true, nil)

	manager := &sshClusterManager{
		executor:  mockExec,
		configMgr: config.NewManager(clusterConfig.ClusterName),
		config:    clusterConfig,
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
			Status: &v1.ImageRegistryStatus{
				Phase: v1.ImageRegistryPhaseCONNECTED,
			},
		},
		imageService: mockImageRegistryService,
	}

	err := manager.StartNode(context.Background(), "2.2.2.2")
	assert.NoError(t, err)
	mockExec.AssertExpectations(t)
}

func TestSSHClusterManager_StopNode(t *testing.T) {
	setUp(t)
	defer tearDown()

	tests := []struct {
		name               string
		nodeIP             string
		nodeExists         bool
		nodeState          string
		drainNodeError     error
		dockerInstalled    bool
		containerRunning   bool
		stopRayError       error
		stopContainerError error
		expectError        bool
	}{
		{
			name:        "node_not_found",
			nodeIP:      "1.1.1.1",
			nodeExists:  false,
			expectError: false,
		},
		{
			name:             "node_alive_drain_success",
			nodeIP:           "1.1.1.1",
			nodeExists:       true,
			nodeState:        v1.AliveNodeState,
			dockerInstalled:  true,
			containerRunning: true,
			expectError:      false,
		},
		{
			name:           "node_alive_drain_failed",
			nodeIP:         "1.1.1.1",
			nodeExists:     true,
			nodeState:      v1.AliveNodeState,
			drainNodeError: errors.New("drain error"),
			expectError:    true,
		},
		{
			name:            "docker_not_installed",
			nodeIP:          "1.1.1.1",
			nodeExists:      true,
			nodeState:       v1.AliveNodeState,
			dockerInstalled: false,
			expectError:     false,
		},
		{
			name:             "container_not_running",
			nodeIP:           "1.1.1.1",
			nodeExists:       true,
			nodeState:        v1.AliveNodeState,
			dockerInstalled:  true,
			containerRunning: false,
			expectError:      false,
		},
		{
			name:             "stop_ray_failed",
			nodeIP:           "1.1.1.1",
			nodeExists:       true,
			nodeState:        v1.AliveNodeState,
			dockerInstalled:  true,
			containerRunning: true,
			stopRayError:     errors.New("stop ray error"),
			expectError:      true,
		},
		{
			name:               "stop_container_failed",
			nodeIP:             "1.1.1.1",
			nodeExists:         true,
			nodeState:          v1.AliveNodeState,
			dockerInstalled:    true,
			containerRunning:   true,
			stopContainerError: errors.New("stop container error"),
			expectError:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup mocks
			mockExec := new(commandmocks.MockExecutor)
			mockDashboard := new(dashboardmocks.MockDashboardService)

			// Setup node info if exists
			if tt.nodeExists {
				node := &v1.NodeSummary{
					IP: tt.nodeIP,
					Raylet: v1.Raylet{
						NodeID: "node-1",
						State:  tt.nodeState,
					},
				}
				mockDashboard.On("ListNodes").Return([]v1.NodeSummary{*node}, nil)
			} else {
				mockDashboard.On("ListNodes").Return([]v1.NodeSummary{}, nil)
			}

			// Setup drain node mock if needed
			if tt.nodeExists && tt.nodeState == v1.AliveNodeState {
				mockExec.On("Execute", mock.Anything, "ray", mock.Anything).Return([]byte{}, tt.drainNodeError).Once()
			}

			// Setup docker check mocks
			if tt.nodeExists && tt.drainNodeError == nil {
				if tt.dockerInstalled {
					mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Return([]byte("docker"), nil).Once()
				} else {
					mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Return([]byte(""), nil).Once()
				}

				// Setup container status mock
				if tt.dockerInstalled {
					mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Return([]byte(fmt.Sprintf("%t", tt.containerRunning)), nil).Once()
				}

				// Setup stop ray mock
				if tt.dockerInstalled && tt.containerRunning {
					mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Return([]byte{}, tt.stopRayError).Once()
				}

				// Setup stop container mock
				if tt.dockerInstalled && tt.containerRunning && tt.stopRayError == nil {
					mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Return([]byte{}, tt.stopContainerError).Once()
				}
			}

			// Setup test manager
			clusterConfig := &v1.RayClusterConfig{
				ClusterName: "test-cluster",
				Provider: v1.Provider{
					HeadIP: "2.2.2.2",
				},
				Auth: v1.Auth{
					SSHUser:       "user",
					SSHPrivateKey: "dGVzdAo=",
				},
				Docker: v1.Docker{
					ContainerName: "test-container",
				},
			}

			manager := &sshClusterManager{
				executor:  mockExec,
				configMgr: config.NewManager(clusterConfig.ClusterName),
				config:    clusterConfig,
			}

			dashboard.NewDashboardService = func(dashboardURL string) dashboard.DashboardService {
				return mockDashboard
			}

			// Execute test
			err := manager.StopNode(context.Background(), tt.nodeIP)

			// Verify results
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			mockExec.AssertExpectations(t)
			mockDashboard.AssertExpectations(t)
		})
	}
}

func TestGetNodeByIP(t *testing.T) {
	tests := []struct {
		name        string
		nodeIP      string
		setupMock   func(*dashboardmocks.MockDashboardService)
		expected    *v1.NodeSummary
		expectError bool
	}{
		{
			name:   "success - node found",
			nodeIP: "192.168.1.1",
			setupMock: func(mockService *dashboardmocks.MockDashboardService) {
				mockService.On("ListNodes").Return([]v1.NodeSummary{
					{
						IP: "192.168.1.1",
						Raylet: v1.Raylet{
							IsHeadNode: false,
							State:      v1.AliveNodeState,
						},
					},
				}, nil)
			},
			expected: &v1.NodeSummary{
				IP: "192.168.1.1",
				Raylet: v1.Raylet{
					IsHeadNode: false,
					State:      v1.AliveNodeState,
				},
			},
			expectError: false,
		},
		{
			name:   "error - node not found",
			nodeIP: "192.168.1.2",
			setupMock: func(mockService *dashboardmocks.MockDashboardService) {
				mockService.On("ListNodes").Return([]v1.NodeSummary{
					{
						IP: "192.168.1.1",
						Raylet: v1.Raylet{
							IsHeadNode: false,
							State:      v1.AliveNodeState,
						},
					},
				}, nil)
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

			dashboard.NewDashboardService = func(dashboardURL string) dashboard.DashboardService {
				return mockService
			}

			ssh := &sshClusterManager{
				config: &v1.RayClusterConfig{
					Provider: v1.Provider{
						HeadIP: "2.2.2.2",
					},
				},
			}

			node, err := ssh.getNodeByIP(context.Background(), tt.nodeIP)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, node)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, node)
			}

			mockService.AssertExpectations(t)
		})
	}
}

func TestGenerateRayClusterConfig(t *testing.T) {
	tests := []struct {
		name           string
		cluster        *v1.Cluster
		imageRegistry  *v1.ImageRegistry
		inputConfig    *v1.RayClusterConfig
		expectedConfig *v1.RayClusterConfig
		expectError    bool
	}{
		{
			name: "success - with minimal input",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{Name: "test-cluster"},
				Spec: &v1.ClusterSpec{
					Version: "v1.0.0",
					Config: map[string]interface{}{
						"auth": map[string]interface{}{
							"ssh_user": "root",
						},
					},
				},
			},
			imageRegistry: &v1.ImageRegistry{
				Spec: &v1.ImageRegistrySpec{
					URL:        "http://registry.example.com",
					Repository: "neutree",
					AuthConfig: v1.ImageRegistryAuthConfig{
						Username: "user",
						Password: "pass",
					},
					Ca: "Y2EK",
				},
			},
			inputConfig: &v1.RayClusterConfig{
				ClusterName: "test-cluster",
			},
			expectedConfig: &v1.RayClusterConfig{
				ClusterName: "test-cluster",
				Provider: v1.Provider{
					Type: "local",
				},
				Docker: v1.Docker{
					ContainerName: "ray_container",
					PullBeforeRun: true,
					Image:         "registry.example.com/neutree/neutree-serve:v1.0.0",
				},
				HeadStartRayCommands: []string{
					"ray stop",
					`ray start --disable-usage-stats --head --metrics-export-port=54311 --port=6379 --object-manager-port=8076 --autoscaling-config=~/ray_bootstrap_config.yaml --dashboard-host=0.0.0.0 --labels='{"neutree.ai/neutree-serving-version":"v1.0.0"}'`,
				},
				WorkerStartRayCommands: []string{
					"ray stop",
					`python /home/ray/start.py $RAY_HEAD_IP --metrics-export-port=54311 --disable-usage-stats --labels='{"neutree.ai/node-provision-type":"autoscaler","neutree.ai/neutree-serving-version":"v1.0.0"}'`,
				},
				StaticWorkerStartRayCommands: []string{
					"ray stop",
					`python /home/ray/start.py $RAY_HEAD_IP --metrics-export-port=54311 --disable-usage-stats --labels='{"neutree.ai/node-provision-type":"static","neutree.ai/neutree-serving-version":"v1.0.0"}'`,
				},
				InitializationCommands: []string{
					"docker login registry.example.com -u 'user' -p 'pass'",
				},
			},
			expectError: false,
		},
		{
			name: "success - always use neutree cluster name",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{Name: "test-cluster"},
				Spec: &v1.ClusterSpec{
					Version: "v1.0.0",
					Config: map[string]interface{}{
						"auth": map[string]interface{}{
							"ssh_user": "root",
						},
					},
				},
			},
			imageRegistry: &v1.ImageRegistry{
				Spec: &v1.ImageRegistrySpec{
					URL:        "http://registry.example.com",
					Repository: "neutree",
					AuthConfig: v1.ImageRegistryAuthConfig{
						Username: "user",
						Password: "pass",
					},
					Ca: "Y2EK",
				},
			},
			inputConfig: &v1.RayClusterConfig{
				ClusterName: "test-cluster-1",
			},
			expectedConfig: &v1.RayClusterConfig{
				ClusterName: "test-cluster",
				Provider: v1.Provider{
					Type: "local",
				},
				Docker: v1.Docker{
					ContainerName: "ray_container",
					PullBeforeRun: true,
					Image:         "registry.example.com/neutree/neutree-serve:v1.0.0",
				},
				HeadStartRayCommands: []string{
					"ray stop",
					`ray start --disable-usage-stats --head --metrics-export-port=54311 --port=6379 --object-manager-port=8076 --autoscaling-config=~/ray_bootstrap_config.yaml --dashboard-host=0.0.0.0 --labels='{"neutree.ai/neutree-serving-version":"v1.0.0"}'`,
				},
				WorkerStartRayCommands: []string{
					"ray stop",
					`python /home/ray/start.py $RAY_HEAD_IP --metrics-export-port=54311 --disable-usage-stats --labels='{"neutree.ai/node-provision-type":"autoscaler","neutree.ai/neutree-serving-version":"v1.0.0"}'`,
				},
				StaticWorkerStartRayCommands: []string{
					"ray stop",
					`python /home/ray/start.py $RAY_HEAD_IP --metrics-export-port=54311 --disable-usage-stats --labels='{"neutree.ai/node-provision-type":"static","neutree.ai/neutree-serving-version":"v1.0.0"}'`,
				},
				InitializationCommands: []string{
					"docker login registry.example.com -u 'user' -p 'pass'",
				},
			},
			expectError: false,
		},
		{
			name: "success - always use neutree cluster name",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{Name: "test-cluster"},
				Spec: &v1.ClusterSpec{
					Version: "v1.0.0",
					Config: map[string]interface{}{
						"auth": map[string]interface{}{
							"ssh_user": "root",
						},
					},
				},
			},
			imageRegistry: &v1.ImageRegistry{
				Spec: &v1.ImageRegistrySpec{
					URL:        "http://registry.example.com",
					Repository: "neutree",
					AuthConfig: v1.ImageRegistryAuthConfig{
						Username: "user",
						Password: "pass",
					},
					Ca: "Y2EK",
				},
			},
			inputConfig: &v1.RayClusterConfig{
				ClusterName: "test-cluster-1",
			},
			expectedConfig: &v1.RayClusterConfig{
				ClusterName: "test-cluster",
				Provider: v1.Provider{
					Type: "local",
				},
				Docker: v1.Docker{
					ContainerName: "ray_container",
					PullBeforeRun: true,
					Image:         "registry.example.com/neutree/neutree-serve:v1.0.0",
				},
				HeadStartRayCommands: []string{
					"ray stop",
					`ray start --disable-usage-stats --head --metrics-export-port=54311 --port=6379 --object-manager-port=8076 --autoscaling-config=~/ray_bootstrap_config.yaml --dashboard-host=0.0.0.0 --labels='{"neutree.ai/neutree-serving-version":"v1.0.0"}'`,
				},
				WorkerStartRayCommands: []string{
					"ray stop",
					`python /home/ray/start.py $RAY_HEAD_IP --metrics-export-port=54311 --disable-usage-stats --labels='{"neutree.ai/node-provision-type":"autoscaler","neutree.ai/neutree-serving-version":"v1.0.0"}'`,
				},
				StaticWorkerStartRayCommands: []string{
					"ray stop",
					`python /home/ray/start.py $RAY_HEAD_IP --metrics-export-port=54311 --disable-usage-stats --labels='{"neutree.ai/node-provision-type":"static","neutree.ai/neutree-serving-version":"v1.0.0"}'`,
				},
				InitializationCommands: []string{
					"docker login registry.example.com -u 'user' -p 'pass'",
				},
			},
			expectError: false,
		},
		{
			name: "success - registry without CA",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{Name: "test-cluster"},
				Spec: &v1.ClusterSpec{
					Version: "v1.0.0",
					Config: map[string]interface{}{
						"auth": map[string]interface{}{
							"ssh_user": "root",
						},
					},
				},
			},
			imageRegistry: &v1.ImageRegistry{
				Spec: &v1.ImageRegistrySpec{
					URL:        "http://registry.example.com",
					Repository: "neutree",
					AuthConfig: v1.ImageRegistryAuthConfig{
						Username: "user",
						Password: "pass",
					},
				},
			},
			inputConfig: &v1.RayClusterConfig{
				ClusterName: "test-cluster-1",
			},
			expectedConfig: &v1.RayClusterConfig{
				ClusterName: "test-cluster",
				Provider: v1.Provider{
					Type: "local",
				},
				Docker: v1.Docker{
					ContainerName: "ray_container",
					PullBeforeRun: true,
					Image:         "registry.example.com/neutree/neutree-serve:v1.0.0",
				},
				HeadStartRayCommands: []string{
					"ray stop",
					`ray start --disable-usage-stats --head --metrics-export-port=54311 --port=6379 --object-manager-port=8076 --autoscaling-config=~/ray_bootstrap_config.yaml --dashboard-host=0.0.0.0 --labels='{"neutree.ai/neutree-serving-version":"v1.0.0"}'`,
				},
				WorkerStartRayCommands: []string{
					"ray stop",
					`python /home/ray/start.py $RAY_HEAD_IP --metrics-export-port=54311 --disable-usage-stats --labels='{"neutree.ai/node-provision-type":"autoscaler","neutree.ai/neutree-serving-version":"v1.0.0"}'`,
				},
				StaticWorkerStartRayCommands: []string{
					"ray stop",
					`python /home/ray/start.py $RAY_HEAD_IP --metrics-export-port=54311 --disable-usage-stats --labels='{"neutree.ai/node-provision-type":"static","neutree.ai/neutree-serving-version":"v1.0.0"}'`,
				},
				InitializationCommands: []string{
					"docker login registry.example.com -u 'user' -p 'pass'",
				},
			},
			expectError: false,
		},
		{
			name: "error - invalid registry URL",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{Name: "test-cluster"},
				Spec: &v1.ClusterSpec{
					Version: "v1.0.0",
					Config: map[string]interface{}{
						"auth": map[string]interface{}{
							"ssh_user": "root",
						},
					},
				},
			},
			imageRegistry: &v1.ImageRegistry{
				Spec: &v1.ImageRegistrySpec{
					URL:        "://invalid-url",
					Repository: "neutree",
					AuthConfig: v1.ImageRegistryAuthConfig{
						Username: "user",
						Password: "pass",
					},
				},
			},
			inputConfig: &v1.RayClusterConfig{},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, err := generateRayClusterConfig(tt.cluster, tt.imageRegistry)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedConfig.ClusterName, config.ClusterName)
				assert.Equal(t, tt.expectedConfig.Provider.Type, config.Provider.Type)
				assert.Equal(t, tt.expectedConfig.Docker.ContainerName, config.Docker.ContainerName)
				assert.Equal(t, tt.expectedConfig.Docker.PullBeforeRun, config.Docker.PullBeforeRun)
				assert.Equal(t, tt.expectedConfig.Docker.Image, config.Docker.Image)
				assert.Equal(t, tt.expectedConfig.HeadStartRayCommands, config.HeadStartRayCommands)
				assert.Equal(t, tt.expectedConfig.WorkerStartRayCommands, config.WorkerStartRayCommands)
				assert.Equal(t, tt.expectedConfig.StaticWorkerStartRayCommands, config.StaticWorkerStartRayCommands)
				assert.Equal(t, tt.expectedConfig.InitializationCommands, config.InitializationCommands)
			}
		})
	}
}

func TestEnsureLocalClusterStateFile(t *testing.T) {
	tests := []struct {
		name          string
		tmpDir        string
		clusterConfig *v1.RayClusterConfig
		setup         func(string, *v1.RayClusterConfig) error
		expectError   bool
	}{
		{
			name:   "success with default tmp dir",
			tmpDir: "",
			clusterConfig: &v1.RayClusterConfig{
				ClusterName: "test-cluster",
				Provider: v1.Provider{
					HeadIP: "192.168.1.1",
				},
			},
			expectError: false,
		},
		{
			name:   "success with custom RAY_TMP_DIR",
			tmpDir: "./tmp/custom/",
			clusterConfig: &v1.RayClusterConfig{
				ClusterName: "test-cluster",
				Provider: v1.Provider{
					HeadIP: "192.168.1.2",
				},
			},
			expectError: false,
		},
		{
			name:   "success when state file already exists",
			tmpDir: "",
			clusterConfig: &v1.RayClusterConfig{
				ClusterName: "existing-cluster",
				Provider: v1.Provider{
					HeadIP: "192.168.1.3",
				},
			},
			setup: func(dir string, config *v1.RayClusterConfig) error {
				_ = os.MkdirAll(dir, 0755)
				stateFilePath := filepath.Join(dir, "cluster-"+config.ClusterName+".state")
				state := map[string]v1.LocalNodeStatus{
					config.Provider.HeadIP: {
						Tags: map[string]string{
							"ray-node-type":   "head",
							"ray-node-status": "up-to-date",
						},
						State: "running",
					},
				}
				content, err := json.Marshal(state)
				if err != nil {
					return err
				}
				return os.WriteFile(stateFilePath, content, 0600)
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			if tt.tmpDir != "" {
				os.Setenv("RAY_TMP_DIR", tt.tmpDir)
				tmpDir = tt.tmpDir
			} else {
				os.Setenv("RAY_TMP_DIR", tmpDir)
			}

			defer os.Unsetenv("RAY_TMP_DIR")

			rayTmpDir := filepath.Join(tmpDir, "ray")

			if tt.setup != nil {
				err := tt.setup(rayTmpDir, tt.clusterConfig)
				assert.NoError(t, err, "setup failed")
			}

			err := ensureLocalClusterStateFile(tt.clusterConfig)
			defer os.RemoveAll(tmpDir)

			if tt.expectError {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)

			_, err = os.Stat(rayTmpDir)
			assert.NoError(t, err, "directory should exist")

			stateFilePath := filepath.Join(rayTmpDir, "cluster-"+tt.clusterConfig.ClusterName+".state")
			_, err = os.Stat(stateFilePath)
			assert.NoError(t, err, "state file should exist")

			if tt.name == "success when state file already exists" {
				originalContent, err := os.ReadFile(stateFilePath)
				assert.NoError(t, err)

				var originalState map[string]v1.LocalNodeStatus
				err = json.Unmarshal(originalContent, &originalState)
				assert.NoError(t, err)

				nodeStatus, exists := originalState[tt.clusterConfig.Provider.HeadIP]
				assert.True(t, exists, "head node status should exist")
				assert.Equal(t, "head", nodeStatus.Tags["ray-node-type"])
				assert.Equal(t, "up-to-date", nodeStatus.Tags["ray-node-status"])
				assert.Equal(t, "running", nodeStatus.State)
			}
		})
	}
}

func TestBuildSSHCommandArgs(t *testing.T) {
	clusterConfig := &v1.RayClusterConfig{
		ClusterName: "test-cluster",
		Auth: v1.Auth{
			SSHUser:       "user",
			SSHPrivateKey: "dGVzdAo=",
		},
	}

	mockExec := &commandmocks.MockExecutor{}

	manager := &sshClusterManager{
		config:    clusterConfig,
		configMgr: config.NewManager(clusterConfig.ClusterName),
		executor:  mockExec,
	}

	args := manager.buildSSHCommandArgs("1.1.1.1")
	assert.Equal(t, "1.1.1.1", args.NodeID)
	assert.Equal(t, "1.1.1.1", args.SshIP)
	assert.Equal(t, "user", args.AuthConfig.SSHUser)
	assert.Equal(t, "test-cluster", args.ClusterName)
}

func TestGetRayTmpDir(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		expected string
	}{
		{
			name:     "default value",
			envValue: "",
			expected: "/tmp/ray",
		},
		{
			name:     "custom value",
			envValue: "/custom/path",
			expected: "/custom/path/ray",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envValue != "" {
				os.Setenv("RAY_TMP_DIR", tt.envValue)
				defer os.Unsetenv("RAY_TMP_DIR")
			}

			result := getRayTmpDir()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func setUp(t *testing.T) {
	os.Setenv("RAY_TMP_DIR", t.TempDir())
}

func tearDown() {
	os.Unsetenv("RAY_TMP_DIR")
}
