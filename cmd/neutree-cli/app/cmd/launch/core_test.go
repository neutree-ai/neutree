package launch

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/constants"
	"github.com/neutree-ai/neutree/pkg/command/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestNewNeutreeCoreInstallCmd(t *testing.T) {
	// Setup test cases
	tests := []struct {
		name           string
		setup          func(*mocks.MockExecutor)
		commonOptions  *commonOptions
		expectedFields map[string]string
	}{
		{
			name: "successful command creation with default values",
			setup: func(mock *mocks.MockExecutor) {
				// No expectations needed for command creation
			},
			commonOptions: &commonOptions{
				workDir:        filepath.Join(os.TempDir(), "neutree-test"),
				deployType:     "local",
				mirrorRegistry: "",
			},
			expectedFields: map[string]string{
				"jwt-secret":               "mDCvM4zSk0ghmpyKhgqWb0g4igcOP0Lp",
				"metrics-remote-write-url": "",
				"version":                  "v0.0.1",
			},
		},
		{
			name: "successful command creation with custom work dir",
			setup: func(mock *mocks.MockExecutor) {
				// No expectations needed for command creation
			},
			commonOptions: &commonOptions{
				workDir:        filepath.Join(os.TempDir(), "custom-neutree-test"),
				deployType:     "local",
				mirrorRegistry: "",
			},
			expectedFields: map[string]string{
				"jwt-secret":               "mDCvM4zSk0ghmpyKhgqWb0g4igcOP0Lp",
				"metrics-remote-write-url": "",
				"version":                  "v0.0.1",
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
			cmd := NewNeutreeCoreInstallCmd(mockExecutor, tt.commonOptions)

			// Verify command properties
			assert.Equal(t, "neutree-core", cmd.Use)
			assert.Equal(t, "Install Neutree Core Services", cmd.Short)
			assert.Contains(t, cmd.Long, "Install and configure the core components of Neutree platform")

			// Verify flags
			for flag, expectedValue := range tt.expectedFields {
				flagValue, err := cmd.PersistentFlags().GetString(flag)
				require.NoError(t, err)
				assert.Equal(t, expectedValue, flagValue)
			}

			// Verify RunE function is set
			assert.NotNil(t, cmd.RunE)
		})
	}
}

func TestInstallNeutreeCoreSingleNodeByDocker_Success(t *testing.T) {
	// Setup test environment
	tempDir := t.TempDir()
	t.Setenv("NEUTREE_LAUNCH_WORK_DIR", tempDir)

	// Prepare mock executor
	mockExecutor := &mocks.MockExecutor{}
	mockExecutor.On("Execute",
		context.Background(),
		"docker",
		[]string{"compose", "-p", "neutree-core", "-f", filepath.Join(tempDir, "neutree-core", "docker-compose.yaml"), "up", "-d"},
	).Return([]byte("success"), nil)

	// Prepare test options
	options := neutreeCoreInstallOptions{
		commonOptions: &commonOptions{
			workDir: tempDir,
		},
		jwtSecret:             "test_jwt",
		metricsRemoteWriteURL: "http://metrics:8428",
		version:               "v1.0.0",
	}

	// Execute test
	err := installNeutreeCoreSingleNodeByDocker(mockExecutor, options)

	// Verify results
	assert.NoError(t, err)

	// Check directory structure
	expectedDirs := []string{
		filepath.Join(tempDir, "neutree-core"),
		filepath.Join(tempDir, "neutree-core", "metrics"),
	}
	for _, dir := range expectedDirs {
		_, err = os.Stat(dir)
		assert.NoError(t, err)
	}

	// Check compose file content
	composeFilePath := filepath.Join(tempDir, "neutree-core", "docker-compose.yaml")
	content, err := os.ReadFile(composeFilePath)
	assert.NoError(t, err)
	assert.Contains(t, string(content), options.jwtSecret)
	assert.Contains(t, string(content), options.metricsRemoteWriteURL)
	assert.Contains(t, string(content), options.version)
	assert.Contains(t, string(content), constants.VictoriaMetricsVersion)

	// Verify mock expectations
	mockExecutor.AssertExpectations(t)
}

func TestInstallNeutreeCoreSingleNodeByDocker_ErrorCases(t *testing.T) {
	tests := []struct {
		name        string
		mockSetup   func(*mocks.MockExecutor)
		options     neutreeCoreInstallOptions
		expectedErr string
	}{
		{
			name: "docker compose failed",
			mockSetup: func(m *mocks.MockExecutor) {
				m.On("Execute", mock.Anything, "docker", mock.Anything).
					Return([]byte("error"), assert.AnError)
			},
			options: neutreeCoreInstallOptions{
				commonOptions: &commonOptions{
					workDir: t.TempDir(),
				},
			},
			expectedErr: "error when executing docker compose up",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup environment

			t.Setenv("NEUTREE_LAUNCH_WORK_DIR", tt.options.workDir)

			mockExecutor := &mocks.MockExecutor{}
			if tt.mockSetup != nil {
				tt.mockSetup(mockExecutor)
			}

			err := installNeutreeCoreSingleNodeByDocker(mockExecutor, tt.options)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedErr)
		})
	}
}
