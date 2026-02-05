package launch

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/constants"
	"github.com/neutree-ai/neutree/pkg/command/mocks"
	"github.com/pkg/errors"
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
				workDir:        filepath.Join(t.TempDir(), "neutree-test"),
				deployType:     "local",
				mirrorRegistry: "",
			},
			expectedFields: map[string]string{
				"jwt-secret":               "",
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
				workDir:        filepath.Join(t.TempDir(), "custom-neutree-test"),
				deployType:     "local",
				mirrorRegistry: "",
			},
			expectedFields: map[string]string{
				"jwt-secret":               "",
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

func TestPrepareNeutreeCoreDeployConfig(t *testing.T) {
	// Setup test cases
	tests := []struct {
		name        string
		options     neutreeCoreInstallOptions
		wantErr     bool
		expectedErr string
	}{
		{
			name: "success with default options",
			options: neutreeCoreInstallOptions{
				commonOptions: &commonOptions{
					workDir:    t.TempDir(),
					nodeIP:     "192.168.1.1",
					deployType: constants.DeployTypeLocal,
					deployMode: constants.DeployModeSingle,
				},
				jwtSecret:             "test-secret",
				metricsRemoteWriteURL: "",
				version:               "v1.0.0",
			},
			wantErr: false,
		},
		{
			name: "success with custom metrics URL",
			options: neutreeCoreInstallOptions{
				commonOptions: &commonOptions{
					workDir:    t.TempDir(),
					nodeIP:     "192.168.1.1",
					deployType: constants.DeployTypeLocal,
					deployMode: constants.DeployModeSingle,
				},
				jwtSecret:             "test-secret",
				metricsRemoteWriteURL: "http://metrics.example.com",
				version:               "v1.0.0",
			},
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
			err = prepareNeutreeCoreDeployConfig(tt.options)

			// Verify results
			if tt.wantErr {
				assert.Error(t, err)
				if tt.expectedErr != "" {
					assert.Contains(t, err.Error(), tt.expectedErr)
				}
			} else {
				assert.NoError(t, err)

				// Verify files were created/modified
				assert.FileExists(t, filepath.Join(tempDir, "neutree-core", "docker-compose.yml"))
			}
		})
	}
}

func TestInstallNeutreeCoreSingleNodeByDocker(t *testing.T) {
	// Setup test cases
	tests := []struct {
		name        string
		options     neutreeCoreInstallOptions
		setupMock   func(*mocks.MockExecutor)
		wantErr     bool
		expectedErr string
	}{
		{
			name: "successful deployment",
			options: neutreeCoreInstallOptions{
				commonOptions: &commonOptions{
					workDir:    t.TempDir(),
					nodeIP:     "192.168.1.1",
					deployType: constants.DeployTypeLocal,
					deployMode: constants.DeployModeSingle,
				},
				jwtSecret: "test-secret",
				version:   "v1.0.0",
			},
			setupMock: func(m *mocks.MockExecutor) {
				m.On("Execute", mock.Anything, "docker", mock.MatchedBy(func(args []string) bool {
					return args[0] == "compose" && args[1] == "-p"
				})).Return([]byte("success"), nil)
			},
			wantErr: false,
		},
		{
			name: "failed when docker compose fails",
			options: neutreeCoreInstallOptions{
				commonOptions: &commonOptions{
					workDir:    t.TempDir(),
					nodeIP:     "192.168.1.1",
					deployType: constants.DeployTypeLocal,
					deployMode: constants.DeployModeSingle,
				},
			},
			setupMock: func(m *mocks.MockExecutor) {
				m.On("Execute", mock.Anything, "docker", mock.Anything).Return([]byte("error"), errors.New("docker error"))
			},
			wantErr:     true,
			expectedErr: "error when executing docker compose up",
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

			// Execute test
			err = installNeutreeCoreSingleNodeByDocker(mockExecutor, tt.options)

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
