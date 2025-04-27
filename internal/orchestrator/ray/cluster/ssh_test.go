package cluster

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/orchestrator/ray/config"
	"github.com/neutree-ai/neutree/internal/orchestrator/ray/observability"
	registrymocks "github.com/neutree-ai/neutree/internal/registry/mocks"
	commandmocks "github.com/neutree-ai/neutree/pkg/command/mocks"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestNewRaySSHClusterManager(t *testing.T) {
	// Setup temp dir
	tmpDir := os.TempDir()
	os.Setenv("RAY_TMP_DIR", tmpDir)
	defer os.RemoveAll(tmpDir)
	defer os.Unsetenv("RAY_TMP_DIR")

	// Common test objects
	validCluster := &v1.Cluster{
		Metadata: &v1.Metadata{Name: "test-cluster"},
		Spec:     &v1.ClusterSpec{Version: "v1.0.0"},
		Status:   &v1.ClusterStatus{Initialized: true},
	}

	validImageRegistry := &v1.ImageRegistry{
		Spec: &v1.ImageRegistrySpec{
			URL:        "https://registry.example.com",
			Repository: "neutree",
		},
		Status: &v1.ImageRegistryStatus{
			Phase: v1.ImageRegistryPhaseCONNECTED,
		},
	}

	tests := []struct {
		name        string
		cluster     *v1.Cluster
		mockSetup   func(*registrymocks.MockImageService)
		expectError bool
	}{
		{
			name:    "success",
			cluster: validCluster,
			mockSetup: func(ms *registrymocks.MockImageService) {
				ms.On("CheckImageExists", mock.Anything, mock.Anything).Return(true, nil)
			},
			expectError: false,
		},
		{
			name:    "invalid_image_registry",
			cluster: validCluster,
			mockSetup: func(ms *registrymocks.MockImageService) {
				ms.On("CheckImageExists", mock.Anything, mock.Anything).Return(false, nil)
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup mocks
			mockImageSvc := new(registrymocks.MockImageService)
			mockExecutor := new(commandmocks.MockExecutor)
			mockObsSync := observability.NewLocalConfigSync(tmpDir)

			if tt.mockSetup != nil {
				tt.mockSetup(mockImageSvc)
			}

			// Execute test
			manager, err := NewRaySSHClusterManager(
				tt.cluster,
				validImageRegistry,
				mockImageSvc,
				mockExecutor,
				mockObsSync,
			)

			// Verify results
			if tt.expectError {
				require.Error(t, err)
				assert.Nil(t, manager)
			} else {
				require.NoError(t, err)
				require.NotNil(t, manager)
				assert.Equal(t, "test-cluster", manager.config.ClusterName)

				// Verify temp files cleanup
				_, err := os.Stat(tmpDir + "/ray_cluster/test-cluster")
				assert.NoError(t, err)
			}

			mockImageSvc.AssertExpectations(t)
		})
	}
}

func TestSSH_DownCluster(t *testing.T) {
	tmpDir := os.TempDir()
	os.Setenv("RAY_TMP_DIR", tmpDir)
	defer os.RemoveAll(tmpDir)
	defer os.Unsetenv("RAY_TMP_DIR")

	testClusterName := "test-cluster"
	testCluster := &v1.Cluster{
		Metadata: &v1.Metadata{Name: testClusterName},
		Spec:     &v1.ClusterSpec{Version: "v1.0.0"},
		Status:   &v1.ClusterStatus{Initialized: true},
	}

	tests := []struct {
		name          string
		mockSetup     func(*commandmocks.MockExecutor)
		setupFiles    func() []string // Return files to verify cleanup
		clusterConfig *v1.RayClusterConfig
		wantErr       bool
		errContains   string
	}{
		{
			name: "Success_With_Multiple_Workers",
			clusterConfig: &v1.RayClusterConfig{
				ClusterName: testClusterName,
				Provider: v1.Provider{
					WorkerIPs: []string{"192.168.1.2", "192.168.1.3"},
				},
			},
			mockSetup: func(m *commandmocks.MockExecutor) {
				m.On("Execute", mock.Anything, "ray", mock.MatchedBy(func(args []string) bool {
					return args[0] == "down"
				})).Return([]byte("success"), nil)
			},
			setupFiles: func() []string {
				files := []string{
					filepath.Join(tmpDir, "ray", "cluster", "cluster-"+testClusterName+".state"),
				}
				for _, f := range files {
					os.WriteFile(f, []byte("test"), 0644)
				}

				return files
			},
			wantErr: false,
		},
		{
			name: "Command_Execution_Failure",
			clusterConfig: &v1.RayClusterConfig{
				ClusterName: testClusterName,
				Provider:    v1.Provider{WorkerIPs: []string{}},
			},
			mockSetup: func(m *commandmocks.MockExecutor) {
				m.On("Execute", mock.Anything, "ray", mock.Anything).
					Return([]byte(""), errors.New("permission denied"))
			},
			wantErr:     true,
			errContains: "failed to down cluster",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup temp files
			if tt.setupFiles != nil {
				tt.setupFiles()
			}

			// Setup mocks
			mockExec := new(commandmocks.MockExecutor)
			tt.mockSetup(mockExec)

			// Setup test manager
			manager := &sshClusterManager{
				executor:      mockExec,
				config:        tt.clusterConfig,
				obsConfigSync: observability.NewLocalConfigSync(tmpDir),
				cluster:       testCluster,
				configMgr:     config.NewManager(tt.clusterConfig.ClusterName),
			}

			// Execute
			err := manager.DownCluster(context.Background())

			// Verify
			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)

				// Verify all files cleanup
				if tt.setupFiles != nil {
					for _, f := range tt.setupFiles() {
						_, err := os.Stat(f)
						assert.True(t, os.IsNotExist(err), "file %s should be removed", f)
					}
				}
			}

			mockExec.AssertExpectations(t)
		})
	}
}
