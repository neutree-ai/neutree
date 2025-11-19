package launch

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/constants"
	"github.com/neutree-ai/neutree/pkg/command/mocks"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"

	"github.com/stretchr/testify/require"
)

func TestNewObsStackInstallCmd(t *testing.T) {
	// Setup test cases
	tests := []struct {
		name          string
		setup         func(*mocks.MockExecutor)
		commonOptions *commonOptions
		expectedCmd   func(*testing.T, *cobra.Command)
	}{
		{
			name: "successful command creation with default options",
			setup: func(mock *mocks.MockExecutor) {
				// No expectations needed for command creation
			},
			commonOptions: &commonOptions{
				workDir:        filepath.Join(t.TempDir(), "neutree-obs-test"),
				deployType:     "local",
				mirrorRegistry: "",
			},
			expectedCmd: func(t *testing.T, cmd *cobra.Command) {
				assert.Equal(t, "obs-stack", cmd.Use)
				assert.Equal(t, "Deploy Neutree Observability Stack", cmd.Short)
				assert.Contains(t, cmd.Long, "Deploy the Neutree Observability Stack")
				assert.NotNil(t, cmd.RunE)
			},
		},
		{
			name: "successful command creation with custom registry",
			setup: func(mock *mocks.MockExecutor) {
				// No expectations needed for command creation
			},
			commonOptions: &commonOptions{
				workDir:        filepath.Join(t.TempDir(), "neutree-obs-test-custom"),
				deployType:     "local",
				mirrorRegistry: "my.registry.com",
			},
			expectedCmd: func(t *testing.T, cmd *cobra.Command) {
				assert.Equal(t, "obs-stack", cmd.Use)
				assert.Equal(t, "Deploy Neutree Observability Stack", cmd.Short)
				assert.Contains(t, cmd.Long, "Deploy the Neutree Observability Stack")
				assert.NotNil(t, cmd.RunE)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock executor
			mockExecutor := &mocks.MockExecutor{}
			if tt.setup != nil {
				tt.setup(mockExecutor)
			}

			// Create command
			cmd := NewObsStackInstallCmd(mockExecutor, tt.commonOptions)

			// Verify command properties
			tt.expectedCmd(t, cmd)
		})
	}
}

func TestPrepareObsStackDeployConfig(t *testing.T) {
	// Setup test cases
	tests := []struct {
		name        string
		options     *obsStackInstallOptions
		setup       func(*mocks.MockExecutor)
		wantErr     bool
		expectedErr string
	}{
		{
			name: "success with default options",
			options: &obsStackInstallOptions{
				commonOptions: &commonOptions{
					workDir:    t.TempDir(),
					nodeIP:     "192.168.1.1",
					deployType: constants.DeployTypeLocal,
					deployMode: constants.DeployModeSingle,
				},
			},
			setup:   func(m *mocks.MockExecutor) {},
			wantErr: false,
		},
		{
			name: "success with custom registry",
			options: &obsStackInstallOptions{
				commonOptions: &commonOptions{
					workDir:        t.TempDir(),
					nodeIP:         "192.168.1.1",
					deployType:     constants.DeployTypeLocal,
					deployMode:     constants.DeployModeSingle,
					mirrorRegistry: "my.registry.com",
				},
			},
			setup:   func(m *mocks.MockExecutor) {},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temp directory for test
			tempDir, err := os.MkdirTemp("", "neutree-test-")
			require.NoError(t, err)
			defer os.RemoveAll(tempDir)

			// Update workDir to use temp directory
			tt.options.workDir = tempDir

			// Execute test
			err = prepareObsStackDeployConfig(tt.options)

			// Verify results
			if tt.wantErr {
				assert.Error(t, err)
				if tt.expectedErr != "" {
					assert.Contains(t, err.Error(), tt.expectedErr)
				}
			} else {
				assert.NoError(t, err)

				// Verify files were created/modified
				assert.FileExists(t, filepath.Join(tempDir, "obs-stack", "docker-compose.yml"))
				assert.FileExists(t, filepath.Join(tempDir, "obs-stack", "grafana", "provisioning", "datasources", "cluster.yml"))
			}
		})
	}
}

func TestInstallObsStackSingleNodeByDocker(t *testing.T) {
	// Setup test cases
	tests := []struct {
		name        string
		options     obsStackInstallOptions
		hostIP      string
		setupMock   func(*mocks.MockExecutor)
		wantErr     bool
		expectedErr string
	}{
		{
			name: "successful deployment",
			options: obsStackInstallOptions{
				commonOptions: &commonOptions{
					workDir:    t.TempDir(),
					nodeIP:     "192.168.1.1",
					deployType: constants.DeployTypeLocal,
					deployMode: constants.DeployModeSingle,
				},
			},
			hostIP: "192.168.1.1",
			setupMock: func(m *mocks.MockExecutor) {
				// no docker CLI expectations, Compose SDK runner is used
			},
			wantErr: false,
		},
		{
			name: "failed when docker compose fails",
			options: obsStackInstallOptions{
				commonOptions: &commonOptions{
					workDir:    t.TempDir(),
					nodeIP:     "192.168.1.1",
					deployType: constants.DeployTypeLocal,
					deployMode: constants.DeployModeSingle,
				},
			},
			hostIP: "192.168.1.1",
			setupMock: func(m *mocks.MockExecutor) {
				// no docker CLI expectations; we will inject compose runner error in test body
			},
			wantErr:     true,
			expectedErr: "compose up failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temp directory for test
			tempDir, err := os.MkdirTemp("", "neutree-test-")
			require.NoError(t, err)
			defer os.RemoveAll(tempDir)

			// Update workDir to use temp directory
			tt.options.workDir = tempDir

			// Setup mock
			mockExecutor := &mocks.MockExecutor{}
			if tt.setupMock != nil {
				tt.setupMock(mockExecutor)
			}

			// stub pullImagesFromCompose to avoid network/docker dependencies in unit tests
			oldPull := pullImagesFromCompose
			pullImagesFromCompose = func(ctx context.Context, composeFile string) ([]string, error) {
				return []string{}, nil
			}
			defer func() { pullImagesFromCompose = oldPull }()

			// stub compose runner
			oldRunner := composeSDKRunner
			if tt.name == "successful deployment" {
				composeSDKRunner = &fakeComposeRunner{err: nil}
			} else {
				composeSDKRunner = &fakeComposeRunner{err: errors.New("compose failed")}
			}
			defer func() { composeSDKRunner = oldRunner }()

			// Execute test
			err = installObsStackSingleNodeByDocker(mockExecutor, tt.options)

			// Verify results
			if tt.wantErr {
				assert.Error(t, err)
				if tt.expectedErr != "" {
					assert.Contains(t, err.Error(), tt.expectedErr)
				}
			} else {
				assert.NoError(t, err)
				mockExecutor.AssertExpectations(t)
			}
		})
	}
}
