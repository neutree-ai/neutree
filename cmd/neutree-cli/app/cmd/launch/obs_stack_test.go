package launch

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/constants"
	"github.com/neutree-ai/neutree/pkg/command/mocks"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
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
				workDir:        filepath.Join(os.TempDir(), "neutree-obs-test"),
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
				workDir:        filepath.Join(os.TempDir(), "neutree-obs-test-custom"),
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

func TestInstallVictoriaMetricsSingleNodeByDocker_Success(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("NEUTREE_LAUNCH_WORK_DIR", tempDir)

	mockExecutor := &mocks.MockExecutor{}
	mockExecutor.On("Execute",
		context.Background(),
		"docker",
		[]string{"compose", "-p", "victoriametrics", "-f", filepath.Join(tempDir, "victoriametrics", "docker-compose.yaml"), "up", "-d"},
	).Return([]byte("success"), nil)

	options := &commonOptions{
		workDir: tempDir,
	}

	hostIP := "192.168.1.100"

	err := installVictoriaMetricsSingleNodeByDocker(mockExecutor, options, hostIP)

	assert.NoError(t, err)

	composeFilePath := filepath.Join(tempDir, "victoriametrics", "docker-compose.yaml")
	_, err = os.Stat(composeFilePath)
	assert.NoError(t, err)

	content, err := os.ReadFile(composeFilePath)
	assert.NoError(t, err)
	assert.Contains(t, string(content), hostIP)
	assert.Contains(t, string(content), constants.VictoriaMetricsClusterVersion)

	mockExecutor.AssertExpectations(t)
}

func TestInstallVictoriaMetricsSingleNodeByDocker_ErrorCases(t *testing.T) {
	tests := []struct {
		name        string
		mockSetup   func(*mocks.MockExecutor)
		options     *commonOptions
		hostIP      string
		expectedErr string
	}{
		{
			name: "docker compose failed",
			mockSetup: func(m *mocks.MockExecutor) {
				m.On("Execute", mock.Anything, "docker", mock.Anything).
					Return([]byte("error"), assert.AnError)
			},
			options: &commonOptions{
				workDir: t.TempDir(),
			},
			hostIP:      "192.168.1.1",
			expectedErr: "error when executing docker compose up",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("NEUTREE_LAUNCH_WORK_DIR", tt.options.workDir)

			mockExecutor := &mocks.MockExecutor{}
			if tt.mockSetup != nil {
				tt.mockSetup(mockExecutor)
			}

			err := installVictoriaMetricsSingleNodeByDocker(mockExecutor, tt.options, tt.hostIP)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedErr)
		})
	}
}

func TestInstallGrafanaByDocker_Success(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("NEUTREE_LAUNCH_WORK_DIR", tempDir)

	mockExecutor := &mocks.MockExecutor{}
	mockExecutor.On("Execute",
		context.Background(),
		"docker",
		[]string{"compose", "-p", "grafana", "-f", filepath.Join(tempDir, "grafana", "docker-compose.yaml"), "up", "-d"},
	).Return([]byte("success"), nil)

	options := &commonOptions{
		workDir: tempDir,
	}

	hostIP := "192.168.1.100"

	err := installGrafanaSingleNodeByDocker(mockExecutor, options, hostIP)

	assert.NoError(t, err)

	expectedDirs := []string{
		filepath.Join(tempDir, "grafana"),
		filepath.Join(tempDir, "grafana", "provisioning", "datasources"),
		filepath.Join(tempDir, "grafana", "provisioning", "dashboards"),
		filepath.Join(tempDir, "grafana", "dashboards"),
	}
	for _, dir := range expectedDirs {
		_, err = os.Stat(dir)
		assert.NoError(t, err)
	}

	composeFilePath := filepath.Join(tempDir, "grafana", "docker-compose.yaml")
	content, err := os.ReadFile(composeFilePath)
	assert.NoError(t, err)
	assert.Contains(t, string(content), constants.GrafanaVersion)

	mockExecutor.AssertExpectations(t)
}

func TestInstallGrafanaByDocker_ErrorCases(t *testing.T) {
	tests := []struct {
		name        string
		mockSetup   func(*mocks.MockExecutor)
		options     *commonOptions
		hostIP      string
		expectedErr string
	}{
		{
			name: "docker compose failed",
			mockSetup: func(m *mocks.MockExecutor) {
				m.On("Execute", mock.Anything, "docker", mock.Anything).
					Return([]byte("error"), assert.AnError)
			},
			options: &commonOptions{
				workDir: t.TempDir(),
			},
			hostIP:      "192.168.1.1",
			expectedErr: "error when executing docker compose up",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("NEUTREE_LAUNCH_WORK_DIR", tt.options.workDir)

			mockExecutor := &mocks.MockExecutor{}
			if tt.mockSetup != nil {
				tt.mockSetup(mockExecutor)
			}

			err := installGrafanaSingleNodeByDocker(mockExecutor, tt.options, tt.hostIP)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedErr)
		})
	}
}
