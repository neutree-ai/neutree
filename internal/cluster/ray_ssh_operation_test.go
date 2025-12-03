package cluster

import (
	"fmt"
	"path"
	"strings"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	acceleratormocks "github.com/neutree-ai/neutree/internal/accelerator/mocks"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/pointer"

	dashboardmocks "github.com/neutree-ai/neutree/internal/ray/dashboard/mocks"
	commandmocks "github.com/neutree-ai/neutree/pkg/command/mocks"
)

//

func TestMutateAcceleratorRuntimeConfig(t *testing.T) {
	tests := []struct {
		name        string
		input       *v1.RayClusterConfig
		want        *v1.RayClusterConfig
		setupMock   func(*acceleratormocks.MockManager)
		wantErr     bool
		wantChanged bool
	}{
		{
			name: "mutate accelerator runtime config success",
			input: &v1.RayClusterConfig{
				Docker: v1.Docker{
					Image:      "rayproject/ray:latest",
					RunOptions: []string{},
				},
			},
			want: &v1.RayClusterConfig{
				Docker: v1.Docker{
					Image: "rayproject/ray:latest-test",
					RunOptions: []string{
						"--gpus all",
						"-e DRIVER_VERSION=450.80.02",
						"-e CUDA_VERSION=11.0",
					},
				},
			},
			setupMock: func(m *acceleratormocks.MockManager) {
				m.On("GetNodeRuntimeConfig", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(
					v1.RuntimeConfig{
						ImageSuffix: "test",
						Env: map[string]string{
							"DRIVER_VERSION": "450.80.02",
							"CUDA_VERSION":   "11.0",
						},
						Options: []string{
							"--gpus all",
						},
					}, nil)
			},
			wantErr:     false,
			wantChanged: true,
		},
		{
			name: "mutate accelerator runtime config success, never changed",
			input: &v1.RayClusterConfig{
				Docker: v1.Docker{
					Image:      "rayproject/ray:latest",
					RunOptions: []string{},
				},
			},
			want: &v1.RayClusterConfig{
				Docker: v1.Docker{
					Image:      "rayproject/ray:latest",
					RunOptions: []string{},
				},
			},
			setupMock: func(m *acceleratormocks.MockManager) {
				m.On("GetNodeRuntimeConfig", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(v1.RuntimeConfig{},
					nil)
			},
			wantErr:     false,
			wantChanged: false,
		},
		{
			name: "mutate accelerator runtime config not found",
			input: &v1.RayClusterConfig{
				Docker: v1.Docker{
					Image:      "rayproject/ray:latest",
					RunOptions: []string{},
				},
			},
			want: &v1.RayClusterConfig{},
			setupMock: func(m *acceleratormocks.MockManager) {
				m.On("GetNodeRuntimeConfig", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(v1.RuntimeConfig{}, errors.New("not found"))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockAcceleratorManager := &acceleratormocks.MockManager{}
			tt.setupMock(mockAcceleratorManager)
			sshRayClusterReconciler := &sshRayClusterReconciler{
				acceleratorManager: mockAcceleratorManager,
			}

			changed, err := sshRayClusterReconciler.mutateAcceleratorRuntimeConfig(&ReconcileContext{
				sshClusterConfig:    &v1.RaySSHProvisionClusterConfig{},
				sshRayClusterConfig: tt.input,
				Cluster: &v1.Cluster{
					Status: &v1.ClusterStatus{
						AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.StringPtr(),
					},
				},
			}, "127.0.0.1")
			if (err != nil) != tt.wantErr {
				t.Errorf("MutateAcceleratorRuntimeConfig() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if changed != tt.wantChanged {
				t.Errorf("MutateAcceleratorRuntimeConfig() changed = %v, want %v", changed, tt.wantChanged)
			}

			if changed {
				if tt.input.Docker.Image != tt.want.Docker.Image {
					t.Errorf("MutateAcceleratorRuntimeConfig() Image = %v, want %v",
						tt.input.Docker.Image, tt.want.Docker.Image)
				}

				if tt.input.Docker.RunOptions == nil || len(tt.input.Docker.RunOptions) != len(tt.want.Docker.RunOptions) {
					t.Errorf("MutateAcceleratorRuntimeConfig() RunOptions = %v, want %v",
						tt.input.Docker.RunOptions, tt.want.Docker.RunOptions)
				}

				for _, option := range tt.want.Docker.RunOptions {
					found := false
					for _, gotOption := range tt.input.Docker.RunOptions {
						if option == gotOption {
							found = true
							break
						}
					}
					if !found {
						t.Errorf("MutateAcceleratorRuntimeConfig() RunOption = %v, want %v",
							tt.input.Docker.RunOptions, tt.want.Docker.RunOptions)
					}
				}
			}

			mockAcceleratorManager.AssertExpectations(t)
		})
	}
}

func TestUpCluster(t *testing.T) {
	tests := []struct {
		name                    string
		restart                 bool
		setupMock               func([]string, *commandmocks.MockExecutor, *acceleratormocks.MockManager)
		wantErr                 bool
		expectedContainCommands []string
	}{
		{
			name:    "up cluster success",
			restart: false,
			setupMock: func(containComamnds []string, cmdExecutor *commandmocks.MockExecutor, accelManager *acceleratormocks.MockManager) {
				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmdArgs, _ := args.Get(2).([]string)
					cmdArgsStr := strings.Join(cmdArgs, " ")
					for _, containCmd := range containComamnds {
						if !strings.Contains(cmdArgsStr, containCmd) {
							t.Errorf("Expected command to contain %s, but got %s", containCmd, cmdArgsStr)
						}
					}
				}).Return([]byte("success"), nil)
				accelManager.On("GetNodeRuntimeConfig", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(v1.RuntimeConfig{}, nil)
			},
			wantErr:                 false,
			expectedContainCommands: []string{"ray", "up", "--no-restart"},
		},
		{
			name:    "restart cluster success",
			restart: true,
			setupMock: func(containComamnds []string, cmdExecutor *commandmocks.MockExecutor, accelManager *acceleratormocks.MockManager) {
				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmdArgs, _ := args.Get(2).([]string)
					cmdArgsStr := strings.Join(cmdArgs, " ")
					for _, containCmd := range containComamnds {
						if !strings.Contains(cmdArgsStr, containCmd) {
							t.Errorf("Expected command to contain %s, but got %s", containCmd, cmdArgsStr)
						}
					}
				}).Return([]byte("success"), nil)
				accelManager.On("GetNodeRuntimeConfig", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(v1.RuntimeConfig{}, nil)
			},
			wantErr:                 false,
			expectedContainCommands: []string{"ray", "up"},
		},
		{
			name:    "up cluster failed",
			restart: false,
			setupMock: func(containComamnds []string, cmdExecutor *commandmocks.MockExecutor, accelManager *acceleratormocks.MockManager) {
				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmdArgs, _ := args.Get(2).([]string)
					cmdArgsStr := strings.Join(cmdArgs, " ")
					for _, containCmd := range containComamnds {
						if !strings.Contains(cmdArgsStr, containCmd) {
							t.Errorf("Expected command to contain %s, but got %s", containCmd, cmdArgsStr)
						}
					}
				}).Return([]byte("failed"), errors.New("command execution failed"))
				accelManager.On("GetNodeRuntimeConfig", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(v1.RuntimeConfig{}, nil)
			},
			wantErr:                 true,
			expectedContainCommands: []string{"ray", "up", "--no-restart"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockCmdExecutor := &commandmocks.MockExecutor{}
			mockAccelManager := &acceleratormocks.MockManager{}
			tt.setupMock(tt.expectedContainCommands, mockCmdExecutor, mockAccelManager)

			sshRayClusterReconciler := &sshRayClusterReconciler{
				executor:           mockCmdExecutor,
				acceleratorManager: mockAccelManager,
			}

			_, err := sshRayClusterReconciler.upCluster(&ReconcileContext{
				sshRayClusterConfig: &v1.RayClusterConfig{},
				sshClusterConfig: &v1.RaySSHProvisionClusterConfig{
					CommonClusterConfig: v1.CommonClusterConfig{},
				},
				sshConfigGenerator: newRaySSHLocalConfigGenerator("test"),
				Cluster: &v1.Cluster{
					Metadata: &v1.Metadata{
						Name:      "test",
						Workspace: "test",
					},
					Status: &v1.ClusterStatus{
						AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.StringPtr(),
					},
				},
			}, tt.restart)
			if (err != nil) != tt.wantErr {
				t.Errorf("upCluster() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			mockCmdExecutor.AssertExpectations(t)
			mockAccelManager.AssertExpectations(t)
		})
	}
}

func TestStartNode(t *testing.T) {
	tests := []struct {
		name                        string
		setupMock                   func([]string, []string, *commandmocks.MockExecutor, *acceleratormocks.MockManager)
		sshRayClusterConfig         *v1.RayClusterConfig
		wantErr                     bool
		expectedContainInitCommands []string
		expectedStartCommands       []string
	}{
		{
			name: "start node success",
			setupMock: func(expectedContainInitCommands, expectedStartCommands []string, cmdExecutor *commandmocks.MockExecutor, accelManager *acceleratormocks.MockManager) {
				accelManager.On("GetNodeRuntimeConfig", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(v1.RuntimeConfig{}, nil)

				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmdArgs, _ := args.Get(2).([]string)
					cmdArgsStr := strings.Join(cmdArgs, " ")
					if !strings.Contains(cmdArgsStr, "uptime") {
						t.Errorf("Expected command to contain uptime, but got %s", cmdArgsStr)
					}
				}).Return([]byte("success"), nil).Once()

				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmdArgs, _ := args.Get(2).([]string)
					cmdArgsStr := strings.Join(cmdArgs, " ")
					for _, containCmd := range expectedContainInitCommands {
						if !strings.Contains(cmdArgsStr, containCmd) {
							t.Errorf("Expected command to contain %s, but got %s", containCmd, cmdArgsStr)
						}
					}
				}).Return([]byte("success"), nil).Once()

				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmdArgs, _ := args.Get(2).([]string)
					cmdArgsStr := strings.Join(cmdArgs, " ")
					if !strings.Contains(cmdArgsStr, "uptime") {
						t.Errorf("Expected command to contain uptime, but got %s", cmdArgsStr)
					}
				}).Return([]byte("success"), nil).Once()

				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
				}).Return([]byte("docker"), nil).Once()

				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmdArgs, _ := args.Get(2).([]string)
					cmdArgsStr := strings.Join(cmdArgs, " ")
					if !strings.Contains(cmdArgsStr, "uptime") {
						t.Errorf("Expected command to contain uptime, but got %s", cmdArgsStr)
					}
				}).Return([]byte("success"), nil).Once()

				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
				}).Return([]byte("success"), nil).Once()

				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmdArgs, _ := args.Get(2).([]string)
					cmdArgsStr := strings.Join(cmdArgs, " ")
					if !strings.Contains(cmdArgsStr, "uptime") {
						t.Errorf("Expected command to contain uptime, but got %s", cmdArgsStr)
					}
				}).Return([]byte("success"), nil).Once()

				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
				}).Return([]byte("true"), nil).Once()

				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmdArgs, _ := args.Get(2).([]string)
					cmdArgsStr := strings.Join(cmdArgs, " ")
					if !strings.Contains(cmdArgsStr, "uptime") {
						t.Errorf("Expected command to contain uptime, but got %s", cmdArgsStr)
					}
				}).Return([]byte("success"), nil).Once()

				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmdArgs, _ := args.Get(2).([]string)
					cmdArgsStr := strings.Join(cmdArgs, " ")
					for _, containCmd := range expectedStartCommands {
						if !strings.Contains(cmdArgsStr, containCmd) {
							t.Errorf("Expected command to contain %s, but got %s", containCmd, cmdArgsStr)
						}
					}
				}).Return([]byte("success"), nil).Once()

			},
			sshRayClusterConfig: &v1.RayClusterConfig{
				Docker: v1.Docker{
					Image:         "rayproject/ray:latest",
					RunOptions:    []string{},
					PullBeforeRun: true,
				},
				InitializationCommands:       []string{"echo init1"},
				StaticWorkerStartRayCommands: []string{"echo start1"},
			},
			wantErr:                     false,
			expectedContainInitCommands: []string{"echo init1"},
			expectedStartCommands:       []string{"echo start1"},
		},
		{
			name: "start node failed on init commands",
			setupMock: func(expectedContainInitCommands, expectedStartCommands []string, cmdExecutor *commandmocks.MockExecutor, accelManager *acceleratormocks.MockManager) {
				accelManager.On("GetNodeRuntimeConfig", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(v1.RuntimeConfig{}, nil)

				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmdArgs, _ := args.Get(2).([]string)
					cmdArgsStr := strings.Join(cmdArgs, " ")
					if !strings.Contains(cmdArgsStr, "uptime") {
						t.Errorf("Expected command to contain uptime, but got %s", cmdArgsStr)
					}
				}).Return([]byte("success"), nil).Once()

				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmdArgs, _ := args.Get(2).([]string)
					cmdArgsStr := strings.Join(cmdArgs, " ")
					for _, containCmd := range expectedContainInitCommands {
						if !strings.Contains(cmdArgsStr, containCmd) {
							t.Errorf("Expected command to contain %s, but got %s", containCmd, cmdArgsStr)
						}
					}
				}).Return([]byte("success"), assert.AnError).Once()

			},
			sshRayClusterConfig: &v1.RayClusterConfig{
				Docker: v1.Docker{
					Image:         "rayproject/ray:latest",
					RunOptions:    []string{},
					PullBeforeRun: true,
				},
				InitializationCommands:       []string{"echo init1"},
				StaticWorkerStartRayCommands: []string{"echo start1"},
			},
			wantErr:                     true,
			expectedContainInitCommands: []string{"echo init1"},
			expectedStartCommands:       []string{"echo start1"},
		},
		{
			name: "start node failed on start commands",
			setupMock: func(expectedContainInitCommands, expectedStartCommands []string, cmdExecutor *commandmocks.MockExecutor, accelManager *acceleratormocks.MockManager) {
				accelManager.On("GetNodeRuntimeConfig", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(v1.RuntimeConfig{}, nil)

				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmdArgs, _ := args.Get(2).([]string)
					cmdArgsStr := strings.Join(cmdArgs, " ")
					if !strings.Contains(cmdArgsStr, "uptime") {
						t.Errorf("Expected command to contain uptime, but got %s", cmdArgsStr)
					}
				}).Return([]byte("success"), nil).Once()

				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmdArgs, _ := args.Get(2).([]string)
					cmdArgsStr := strings.Join(cmdArgs, " ")
					for _, containCmd := range expectedContainInitCommands {
						if !strings.Contains(cmdArgsStr, containCmd) {
							t.Errorf("Expected command to contain %s, but got %s", containCmd, cmdArgsStr)
						}
					}
				}).Return([]byte("success"), nil).Once()

				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmdArgs, _ := args.Get(2).([]string)
					cmdArgsStr := strings.Join(cmdArgs, " ")
					if !strings.Contains(cmdArgsStr, "uptime") {
						t.Errorf("Expected command to contain uptime, but got %s", cmdArgsStr)
					}
				}).Return([]byte("success"), nil).Once()

				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
				}).Return([]byte("docker"), nil).Once()

				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmdArgs, _ := args.Get(2).([]string)
					cmdArgsStr := strings.Join(cmdArgs, " ")
					if !strings.Contains(cmdArgsStr, "uptime") {
						t.Errorf("Expected command to contain uptime, but got %s", cmdArgsStr)
					}
				}).Return([]byte("success"), nil).Once()

				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
				}).Return([]byte("success"), nil).Once()

				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmdArgs, _ := args.Get(2).([]string)
					cmdArgsStr := strings.Join(cmdArgs, " ")
					if !strings.Contains(cmdArgsStr, "uptime") {
						t.Errorf("Expected command to contain uptime, but got %s", cmdArgsStr)
					}
				}).Return([]byte("success"), nil).Once()

				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
				}).Return([]byte("true"), nil).Once()

				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmdArgs, _ := args.Get(2).([]string)
					cmdArgsStr := strings.Join(cmdArgs, " ")
					if !strings.Contains(cmdArgsStr, "uptime") {
						t.Errorf("Expected command to contain uptime, but got %s", cmdArgsStr)
					}
				}).Return([]byte("success"), nil).Once()

				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmdArgs, _ := args.Get(2).([]string)
					cmdArgsStr := strings.Join(cmdArgs, " ")
					for _, containCmd := range expectedStartCommands {
						if !strings.Contains(cmdArgsStr, containCmd) {
							t.Errorf("Expected command to contain %s, but got %s", containCmd, cmdArgsStr)
						}
					}
				}).Return([]byte("success"), assert.AnError).Once()

			},
			sshRayClusterConfig: &v1.RayClusterConfig{
				Docker: v1.Docker{
					Image:         "rayproject/ray:latest",
					RunOptions:    []string{},
					PullBeforeRun: true,
				},
				InitializationCommands:       []string{"echo init1"},
				StaticWorkerStartRayCommands: []string{"echo start1"},
			},
			wantErr:                     true,
			expectedContainInitCommands: []string{"echo init1"},
			expectedStartCommands:       []string{"echo start1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockCmdExecutor := &commandmocks.MockExecutor{}
			mockAccelManager := &acceleratormocks.MockManager{}
			tt.setupMock(tt.expectedContainInitCommands, tt.expectedStartCommands, mockCmdExecutor, mockAccelManager)

			sshRayClusterReconciler := &sshRayClusterReconciler{
				executor:           mockCmdExecutor,
				acceleratorManager: mockAccelManager,
			}

			err := sshRayClusterReconciler.startNode(&ReconcileContext{
				sshRayClusterConfig: tt.sshRayClusterConfig,
				sshClusterConfig: &v1.RaySSHProvisionClusterConfig{
					CommonClusterConfig: v1.CommonClusterConfig{
						AcceleratorType: pointer.String("gpu"),
					},
				},
				sshConfigGenerator: newRaySSHLocalConfigGenerator("test"),
				Cluster: &v1.Cluster{
					Status: &v1.ClusterStatus{
						AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.StringPtr(),
					},
				},
			}, "test-node")
			if (err != nil) != tt.wantErr {
				t.Errorf("startNode() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			mockCmdExecutor.AssertExpectations(t)
			mockAccelManager.AssertExpectations(t)
		})
	}
}

func TestDrainNode(t *testing.T) {
	tests := []struct {
		name      string
		setupMock func(*commandmocks.MockExecutor)
		wantErr   bool
	}{
		{
			name: "drain node success",
			setupMock: func(e *commandmocks.MockExecutor) {
				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmdArgs, _ := args.Get(2).([]string)
					cmdArgsStr := strings.Join(cmdArgs, " ")
					if !strings.Contains(cmdArgsStr, "drain-node") {
						t.Errorf("Expected command to contain drain-node, but got %s", cmdArgsStr)
					}
				}).Return([]byte("success"), nil)
			},
			wantErr: false,
		},
		{
			name: "drain node failed",
			setupMock: func(e *commandmocks.MockExecutor) {
				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmdArgs, _ := args.Get(2).([]string)
					cmdArgsStr := strings.Join(cmdArgs, " ")
					if !strings.Contains(cmdArgsStr, "drain-node") {
						t.Errorf("Expected command to contain drain-node, but got %s", cmdArgsStr)
					}
				}).Return([]byte("failed"), errors.New("command execution failed"))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockCmdExecutor := &commandmocks.MockExecutor{}
			tt.setupMock(mockCmdExecutor)

			sshRayClusterReconciler := &sshRayClusterReconciler{
				executor: mockCmdExecutor,
			}

			err := sshRayClusterReconciler.drainNode(&ReconcileContext{
				sshRayClusterConfig: &v1.RayClusterConfig{},
				sshClusterConfig:    &v1.RaySSHProvisionClusterConfig{},
				sshConfigGenerator:  newRaySSHLocalConfigGenerator("test"),
			}, "test-node", "test", "test", 600)
			if (err != nil) != tt.wantErr {
				t.Errorf("drainNode() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			mockCmdExecutor.AssertExpectations(t)
		})
	}
}

func TestGetNodeByIP(t *testing.T) {
	tests := []struct {
		name      string
		setupMock func(*dashboardmocks.MockDashboardService)
		wantErr   bool
	}{
		{
			name: "get node success",
			setupMock: func(m *dashboardmocks.MockDashboardService) {
				m.On("ListNodes").Return([]v1.NodeSummary{
					{
						IP: "127.0.0.1",
					},
				}, nil)
			},
			wantErr: false,
		},
		{
			name: "get node failed",
			setupMock: func(m *dashboardmocks.MockDashboardService) {
				m.On("ListNodes").Return([]v1.NodeSummary{
					{
						IP: "127.0.0.1",
					},
				}, assert.AnError)
			},
			wantErr: true,
		},
		{
			name: " node not found",
			setupMock: func(m *dashboardmocks.MockDashboardService) {
				m.On("ListNodes").Return([]v1.NodeSummary{
					{
						IP: "127.0.0.2",
					},
				}, nil)
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockDashboardService := &dashboardmocks.MockDashboardService{}
			tt.setupMock(mockDashboardService)

			sshRayClusterReconciler := &sshRayClusterReconciler{}

			_, err := sshRayClusterReconciler.getNodeByIP(&ReconcileContext{
				rayService: mockDashboardService,
			}, "127.0.0.1")
			if (err != nil) != tt.wantErr {
				t.Errorf("getNodeByIP() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			mockDashboardService.AssertExpectations(t)
		})
	}
}

func TestStopNode(t *testing.T) {
	tests := []struct {
		name      string
		setupMock func(*commandmocks.MockExecutor)
		wantErr   bool
		forceStop bool
	}{
		{
			name: "stop node success for docker not installed",
			setupMock: func(cmdExecutor *commandmocks.MockExecutor) {
				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmdArgs, _ := args.Get(2).([]string)
					cmdArgsStr := strings.Join(cmdArgs, " ")
					if !strings.Contains(cmdArgsStr, "uptime") {
						t.Errorf("Expected command to contain uptime, but got %s", cmdArgsStr)
					}
				}).Return([]byte("success"), nil).Once()

				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
				}).Return([]byte("not found"), nil).Once()
			},
			wantErr:   false,
			forceStop: true,
		},
		{
			name: "stop node failed for check docker failed",
			setupMock: func(cmdExecutor *commandmocks.MockExecutor) {
				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmdArgs, _ := args.Get(2).([]string)
					cmdArgsStr := strings.Join(cmdArgs, " ")
					if !strings.Contains(cmdArgsStr, "uptime") {
						t.Errorf("Expected command to contain uptime, but got %s", cmdArgsStr)
					}
				}).Return([]byte("success"), nil).Once()

				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
				}).Return([]byte("not found"), assert.AnError).Once()
			},
			wantErr:   true,
			forceStop: true,
		},
		{
			name: "stop node success for docker container already stop",
			setupMock: func(cmdExecutor *commandmocks.MockExecutor) {
				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmdArgs, _ := args.Get(2).([]string)
					cmdArgsStr := strings.Join(cmdArgs, " ")
					if !strings.Contains(cmdArgsStr, "uptime") {
						t.Errorf("Expected command to contain uptime, but got %s", cmdArgsStr)
					}
				}).Return([]byte("success"), nil).Once()

				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
				}).Return([]byte("docker"), nil).Once()
				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmdArgs, _ := args.Get(2).([]string)
					cmdArgsStr := strings.Join(cmdArgs, " ")
					if !strings.Contains(cmdArgsStr, "uptime") {
						t.Errorf("Expected command to contain uptime, but got %s", cmdArgsStr)
					}
				}).Return([]byte("success"), nil).Once()

				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
				}).Return([]byte("false"), nil).Once()
			},
			wantErr:   false,
			forceStop: true,
		},
		{
			name: "stop node failed for check docker container status failed",
			setupMock: func(cmdExecutor *commandmocks.MockExecutor) {
				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmdArgs, _ := args.Get(2).([]string)
					cmdArgsStr := strings.Join(cmdArgs, " ")
					if !strings.Contains(cmdArgsStr, "uptime") {
						t.Errorf("Expected command to contain uptime, but got %s", cmdArgsStr)
					}
				}).Return([]byte("success"), nil).Once()

				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
				}).Return([]byte("docker"), nil).Once()
				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmdArgs, _ := args.Get(2).([]string)
					cmdArgsStr := strings.Join(cmdArgs, " ")
					if !strings.Contains(cmdArgsStr, "uptime") {
						t.Errorf("Expected command to contain uptime, but got %s", cmdArgsStr)
					}
				}).Return([]byte("success"), nil).Once()

				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
				}).Return([]byte("false"), assert.AnError).Once()
			},
			wantErr:   true,
			forceStop: true,
		},
		{
			name: "stop node success",
			setupMock: func(cmdExecutor *commandmocks.MockExecutor) {
				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmdArgs, _ := args.Get(2).([]string)
					cmdArgsStr := strings.Join(cmdArgs, " ")
					if !strings.Contains(cmdArgsStr, "uptime") {
						t.Errorf("Expected command to contain uptime, but got %s", cmdArgsStr)
					}
				}).Return([]byte("success"), nil).Once()

				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
				}).Return([]byte("docker"), nil).Once()
				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmdArgs, _ := args.Get(2).([]string)
					cmdArgsStr := strings.Join(cmdArgs, " ")
					if !strings.Contains(cmdArgsStr, "uptime") {
						t.Errorf("Expected command to contain uptime, but got %s", cmdArgsStr)
					}
				}).Return([]byte("success"), nil).Once()

				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
				}).Return([]byte("true"), nil).Once()
				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmdArgs, _ := args.Get(2).([]string)
					cmdArgsStr := strings.Join(cmdArgs, " ")
					if !strings.Contains(cmdArgsStr, "uptime") {
						t.Errorf("Expected command to contain uptime, but got %s", cmdArgsStr)
					}
				}).Return([]byte("success"), nil).Once()

				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmdArgs, _ := args.Get(2).([]string)
					cmdArgsStr := strings.Join(cmdArgs, "")
					assert.Contains(t, cmdArgsStr, "ray stop")
				}).Return([]byte(""), nil).Once()
				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmdArgs, _ := args.Get(2).([]string)
					cmdArgsStr := strings.Join(cmdArgs, " ")
					if !strings.Contains(cmdArgsStr, "uptime") {
						t.Errorf("Expected command to contain uptime, but got %s", cmdArgsStr)
					}
				}).Return([]byte("success"), nil).Once()

				cmdExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmdArgs, _ := args.Get(2).([]string)
					cmdArgsStr := strings.Join(cmdArgs, "")
					assert.Contains(t, cmdArgsStr, "docker stop")
				}).Return([]byte(""), nil).Once()
			},
			wantErr:   false,
			forceStop: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockCmdExecutor := &commandmocks.MockExecutor{}
			tt.setupMock(mockCmdExecutor)

			sshRayClusterReconciler := &sshRayClusterReconciler{
				executor: mockCmdExecutor,
			}

			err := sshRayClusterReconciler.stopNode(&ReconcileContext{
				sshRayClusterConfig: &v1.RayClusterConfig{},
				sshClusterConfig:    &v1.RaySSHProvisionClusterConfig{},
				sshConfigGenerator:  newRaySSHLocalConfigGenerator("test"),
			}, "test-node", tt.forceStop)
			if (err != nil) != tt.wantErr {
				t.Errorf("stopNode() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			mockCmdExecutor.AssertExpectations(t)
		})
	}
}

func TestDownCluster(t *testing.T) {
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
			mockExec.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
			}).Return(tt.mockExecOutput, tt.mockExecError)

			clusterConfig := &v1.RaySSHProvisionClusterConfig{
				Provider: v1.Provider{
					WorkerIPs: tt.workerIPs,
				},
				Auth: v1.Auth{
					SSHUser:       "user",
					SSHPrivateKey: "dGVzdAo=",
				},
			}

			r := &sshRayClusterReconciler{
				executor: mockExec,
			}

			// Test
			err := r.downCluster(&ReconcileContext{
				sshClusterConfig: clusterConfig,
				sshRayClusterConfig: &v1.RayClusterConfig{
					Docker: v1.Docker{},
				},
				sshConfigGenerator: newRaySSHLocalConfigGenerator("test"),
				Cluster: &v1.Cluster{
					Metadata: &v1.Metadata{
						Name:      "test",
						Workspace: "test",
					},
				},
			})

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

func TestGenerateRayClusterConfig(t *testing.T) {
	defaultExpectedConfig := func() *v1.RayClusterConfig {
		return &v1.RayClusterConfig{
			ClusterName: "test-cluster",
			Provider: v1.Provider{
				Type: "local",
			},
			Auth: v1.Auth{
				SSHUser: "root",
			},
			Docker: v1.Docker{
				ContainerName: "ray_container",
				PullBeforeRun: true,
				Image:         "registry.example.com/neutree/neutree-serve:v1.0.0",
				RunOptions: []string{
					"--privileged",
					"--cap-add=SYS_ADMIN",
					"--security-opt=seccomp=unconfined",
					"-e RAY_kill_child_processes_on_worker_exit_with_raylet_subreaper=true",
					"--ulimit nofile=65536:65536",
					fmt.Sprintf("-e %s=%s", v1.ModelCacheDirENV, v1.DefaultSSHClusterModelCacheMountPath),
				},
			},
			HeadStartRayCommands: []string{
				"ray stop",
				`ulimit -n 65536; python /home/ray/start.py --head --port=6379 --metrics-export-port=54311 --disable-usage-stats --autoscaling-config=~/ray_bootstrap_config.yaml --dashboard-host=0.0.0.0 --labels='{"neutree.ai/neutree-serving-version":"v1.0.0"}'`,
			},
			WorkerStartRayCommands: []string{
				"ray stop",
				`ulimit -n 65536; python /home/ray/start.py --address=$RAY_HEAD_IP:6379 --metrics-export-port=54311 --disable-usage-stats --labels='{"neutree.ai/node-provision-type":"autoscaler","neutree.ai/neutree-serving-version":"v1.0.0"}'`,
			},
			StaticWorkerStartRayCommands: []string{
				"ray stop",
				`ulimit -n 65536; python /home/ray/start.py --address=$RAY_HEAD_IP:6379 --metrics-export-port=54311 --disable-usage-stats --labels='{"neutree.ai/node-provision-type":"static","neutree.ai/neutree-serving-version":"v1.0.0"}'`,
			},
			InitializationCommands: []string{
				"docker login registry.example.com -u 'user' -p 'pass'",
			},
		}
	}
	tests := []struct {
		name           string
		cluster        *v1.Cluster
		imageRegistry  *v1.ImageRegistry
		inputConfig    *v1.RayClusterConfig
		expectedConfig func() *v1.RayClusterConfig
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
					Repository: "",
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
			expectedConfig: func() *v1.RayClusterConfig {
				return defaultExpectedConfig()
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
					Repository: "",
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
			expectedConfig: func() *v1.RayClusterConfig {
				return defaultExpectedConfig()
			},
			expectError: false,
		},
		{
			name: "success - without registry auth",
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
					Repository: "",
					AuthConfig: v1.ImageRegistryAuthConfig{},
					Ca:         "Y2EK",
				},
			},
			inputConfig: &v1.RayClusterConfig{
				ClusterName: "test-cluster-1",
			},
			expectedConfig: func() *v1.RayClusterConfig {
				config := defaultExpectedConfig()
				config.InitializationCommands = []string{}
				return config
			},
			expectError: false,
		},
		{
			name: "success - registry with custom repository",
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
					Repository: "custom-repo",
					AuthConfig: v1.ImageRegistryAuthConfig{
						Username: "user",
						Password: "pass",
					},
				},
			},
			inputConfig: &v1.RayClusterConfig{
				ClusterName: "test-cluster-1",
			},
			expectedConfig: func() *v1.RayClusterConfig {
				config := defaultExpectedConfig()
				config.Docker.Image = "registry.example.com/custom-repo/neutree/neutree-serve:v1.0.0"
				return config
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
			r := sshRayClusterReconciler{}
			sshClusterConfig, _ := util.ParseSSHClusterConfig(tt.cluster)
			config, err := r.generateRayClusterConfig(&ReconcileContext{
				Cluster:          tt.cluster,
				ImageRegistry:    tt.imageRegistry,
				sshClusterConfig: sshClusterConfig,
			})

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				expectedConfig := tt.expectedConfig()
				eq := assert.ObjectsAreEqual(config, expectedConfig)
				if !eq {
					t.Errorf("Generated config does not match expected config.\nGot: %+v\nExpected: %+v", config, expectedConfig)
				}
			}
		})
	}
}

func TestMutateModelCache(t *testing.T) {
	testHostPath := "/mnt/model_cache"
	initPathCmd := fmt.Sprintf("mkdir -p %s && chmod 755 %s", testHostPath, testHostPath)
	modelCacheRunOption := fmt.Sprintf("-e %s=%s", v1.ModelCacheDirENV, v1.DefaultSSHClusterModelCacheMountPath)
	modifyPermissionCommand := fmt.Sprintf("sudo chown -R $(id -u):$(id -g) %s", v1.DefaultSSHClusterModelCacheMountPath)
	tests := []struct {
		name                string
		sshRayClusterConfig *v1.RayClusterConfig
		modelCaches         []v1.ModelCache
		expected            *v1.RayClusterConfig
	}{
		{
			name: "set default command when model cache is nil",
			sshRayClusterConfig: &v1.RayClusterConfig{
				HeadStartRayCommands:         []string{},
				WorkerStartRayCommands:       []string{},
				StaticWorkerStartRayCommands: []string{},
			},
			expected: &v1.RayClusterConfig{
				Docker: v1.Docker{
					RunOptions: []string{modelCacheRunOption},
				},
				HeadStartRayCommands:         []string{},
				WorkerStartRayCommands:       []string{},
				StaticWorkerStartRayCommands: []string{},
			},
		},
		{
			name: "only set default command if model cache host path is nil",
			sshRayClusterConfig: &v1.RayClusterConfig{
				HeadStartRayCommands:         []string{},
				WorkerStartRayCommands:       []string{},
				StaticWorkerStartRayCommands: []string{},
			},
			modelCaches: []v1.ModelCache{
				{
					ModelRegistryType: v1.HuggingFaceModelRegistryType,
					HostPath:          nil,
				},
			},
			expected: &v1.RayClusterConfig{
				Docker: v1.Docker{
					RunOptions: []string{modelCacheRunOption},
				},
				HeadStartRayCommands:         []string{},
				WorkerStartRayCommands:       []string{},
				StaticWorkerStartRayCommands: []string{},
			},
		},
		{
			name: "only set default command if model cache nfs is not nil, ssh cluster only support hostpath",
			sshRayClusterConfig: &v1.RayClusterConfig{
				HeadStartRayCommands:         []string{},
				WorkerStartRayCommands:       []string{},
				StaticWorkerStartRayCommands: []string{},
			},
			modelCaches: []v1.ModelCache{
				{
					ModelRegistryType: v1.HuggingFaceModelRegistryType,
					HostPath:          nil,
					NFS:               &corev1.NFSVolumeSource{Path: testHostPath},
				},
			},
			expected: &v1.RayClusterConfig{
				Docker: v1.Docker{
					RunOptions: []string{modelCacheRunOption},
				},
				HeadStartRayCommands:         []string{},
				WorkerStartRayCommands:       []string{},
				StaticWorkerStartRayCommands: []string{},
			},
		},
		{
			name:                "mutate huggingface type model cache success",
			sshRayClusterConfig: &v1.RayClusterConfig{},
			modelCaches: []v1.ModelCache{
				{
					ModelRegistryType: v1.HuggingFaceModelRegistryType,
					HostPath: &corev1.HostPathVolumeSource{
						Path: testHostPath,
					},
				},
			},
			expected: &v1.RayClusterConfig{
				Docker: v1.Docker{
					RunOptions: []string{
						modelCacheRunOption,
						fmt.Sprintf("--volume %s:%s", testHostPath, path.Join(v1.DefaultSSHClusterModelCacheMountPath, v1.HuggingFaceModelRegistryType)),
					},
				},
				HeadStartRayCommands:         []string{modifyPermissionCommand},
				WorkerStartRayCommands:       []string{modifyPermissionCommand},
				StaticWorkerStartRayCommands: []string{modifyPermissionCommand},
				InitializationCommands:       []string{initPathCmd},
			},
		},
		{
			name:                "mutate bentoml type model cache success",
			sshRayClusterConfig: &v1.RayClusterConfig{},
			modelCaches: []v1.ModelCache{
				{
					ModelRegistryType: v1.BentoMLModelRegistryType,
					HostPath: &corev1.HostPathVolumeSource{
						Path: testHostPath,
					},
				},
			},
			expected: &v1.RayClusterConfig{
				Docker: v1.Docker{
					RunOptions: []string{
						modelCacheRunOption,
						fmt.Sprintf("--volume %s:%s", testHostPath, path.Join(v1.DefaultSSHClusterModelCacheMountPath, v1.BentoMLModelRegistryType)),
					},
				},
				HeadStartRayCommands:         []string{modifyPermissionCommand},
				WorkerStartRayCommands:       []string{modifyPermissionCommand},
				StaticWorkerStartRayCommands: []string{modifyPermissionCommand},
				InitializationCommands:       []string{initPathCmd},
			},
		},
		{
			name:                "mutate both huggingface and bentoml type model cache success",
			sshRayClusterConfig: &v1.RayClusterConfig{},
			modelCaches: []v1.ModelCache{
				{
					ModelRegistryType: v1.HuggingFaceModelRegistryType,
					HostPath: &corev1.HostPathVolumeSource{
						Path: testHostPath,
					},
				},
				{
					ModelRegistryType: v1.BentoMLModelRegistryType,
					HostPath: &corev1.HostPathVolumeSource{
						Path: testHostPath,
					},
				},
			},
			expected: &v1.RayClusterConfig{
				Docker: v1.Docker{
					RunOptions: []string{
						modelCacheRunOption,
						fmt.Sprintf("--volume %s:%s", testHostPath, path.Join(v1.DefaultSSHClusterModelCacheMountPath, v1.HuggingFaceModelRegistryType)),
						fmt.Sprintf("--volume %s:%s", testHostPath, path.Join(v1.DefaultSSHClusterModelCacheMountPath, v1.BentoMLModelRegistryType)),
					},
				},
				HeadStartRayCommands:         []string{modifyPermissionCommand},
				WorkerStartRayCommands:       []string{modifyPermissionCommand},
				StaticWorkerStartRayCommands: []string{modifyPermissionCommand},
				InitializationCommands:       []string{initPathCmd, initPathCmd},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			mutateModelCaches(tt.sshRayClusterConfig, tt.modelCaches)
			eq, _, err := util.JsonEqual(tt.expected, tt.sshRayClusterConfig)
			if err != nil {
				t.Errorf("JsonEqual() error = %v", err)
				return
			}
			if !eq {
				t.Errorf("mutateModelCaches() got = %+v, want %+v", tt.sshRayClusterConfig, tt.expected)
			}

			eq = assert.ObjectsAreEqual(tt.expected.StaticWorkerStartRayCommands, tt.sshRayClusterConfig.StaticWorkerStartRayCommands)
			if !eq {
				t.Errorf("StaticWorkerStartRayCommands not match, got = %+v, want %+v", tt.sshRayClusterConfig.StaticWorkerStartRayCommands, tt.expected.StaticWorkerStartRayCommands)
			}
		})
	}
}
