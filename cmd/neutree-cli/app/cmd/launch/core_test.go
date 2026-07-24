package launch

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/compose-spec/compose-go/cli"
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

func TestPrepareNeutreeCoreDeployConfigRendersKongPluginChecksumLabels(t *testing.T) {
	tempDir := t.TempDir()
	options := neutreeCoreInstallOptions{
		commonOptions: &commonOptions{
			workDir:    tempDir,
			nodeIP:     "192.168.1.1",
			deployType: constants.DeployTypeLocal,
			deployMode: constants.DeployModeSingle,
		},
		jwtSecret: "test-secret",
		version:   "v1.0.0",
	}

	require.NoError(t, prepareNeutreeCoreDeployConfig(options))

	composeFilePath := filepath.Join(tempDir, "neutree-core", "docker-compose.yml")
	project, err := cli.ProjectFromOptions(&cli.ProjectOptions{
		ConfigPaths: []string{composeFilePath},
	})
	require.NoError(t, err)

	expectedChecksums, err := kongPluginChecksums(filepath.Join(tempDir, "neutree-core", "gateway", "kong", "plugins"))
	require.NoError(t, err)
	expectedLabels := map[string]string{
		"neutree.ai/kong-plugin-neutree-ai-gateway-checksum":    expectedChecksums["neutree-ai-gateway"],
		"neutree.ai/kong-plugin-neutree-ai-statistics-checksum": expectedChecksums["neutree-ai-statistics"],
		"neutree.ai/kong-plugin-neutree-ai-access-checksum":     expectedChecksums["neutree-ai-access"],
		"neutree.ai/kong-plugin-neutree-ai-quota-checksum":      expectedChecksums["neutree-ai-quota"],
	}

	var kongLabels map[string]string
	for _, service := range project.Services {
		if service.Name == "kong" {
			kongLabels = service.Labels
			break
		}
	}
	require.NotNil(t, kongLabels)
	for label, checksum := range expectedLabels {
		assert.Equal(t, checksum, kongLabels[label])
	}

	for _, service := range project.Services {
		if service.Name == "kong" {
			continue
		}
		for label := range expectedLabels {
			assert.NotContains(t, service.Labels, label, "unexpected Kong plugin checksum label on %s", service.Name)
		}
	}
}

// NEU-583 regression: rendering the embedded manifests must not HTML-escape
// the Vector VRL program — html/template turned `<=` into `&lt;=`, which
// Vector rejects (exit 78 restart loop), silently disabling the NEU-539
// chunking topology on Docker CP deployments.
func TestPrepareNeutreeCoreDeployConfig_PreservesVRLOperators(t *testing.T) {
	tempDir := t.TempDir()

	options := neutreeCoreInstallOptions{
		commonOptions: &commonOptions{
			workDir:    tempDir,
			nodeIP:     "192.168.1.1",
			deployType: constants.DeployTypeLocal,
			deployMode: constants.DeployModeSingle,
		},
		jwtSecret: "test-secret",
		version:   "v1.0.0",
	}

	require.NoError(t, prepareNeutreeCoreDeployConfig(options))

	for _, f := range []string{
		filepath.Join(tempDir, "neutree-core", "vector", "vector.yml"),
		filepath.Join(tempDir, "neutree-core", "docker-compose.yml"),
	} {
		content, err := os.ReadFile(f)
		require.NoError(t, err)
		assert.NotContainsf(t, string(content), "&lt;", "HTML-escaped operator in %s", f)
		assert.NotContainsf(t, string(content), "&amp;", "HTML-escaped ampersand in %s", f)
		assert.NotContainsf(t, string(content), "&#", "HTML entity in %s", f)
	}

	vector, err := os.ReadFile(filepath.Join(tempDir, "neutree-core", "vector", "vector.yml"))
	require.NoError(t, err)
	// The NEU-539 chunking threshold must survive rendering verbatim.
	assert.Contains(t, string(vector), "<= 1572864")
}

func TestValidateNeutreeCoreVersionCompatibility(t *testing.T) {
	tests := []struct {
		name          string
		cliVersion    string
		targetVersion string
		wantErr       string
	}{
		{
			name:          "allows target version in same v1.1 release line",
			cliVersion:    "v1.1.0-nightly-20260608",
			targetVersion: "v1.1.0-nightly-20260609",
		},
		{
			name:          "allows git describe prerelease version in same release line",
			cliVersion:    "v1.1.0-nightly-20260608-5-g1e6a9fc8",
			targetVersion: "v1.1.0-nightly-20260609",
		},
		{
			name:          "git describe CLI keeps development build flexibility",
			cliVersion:    "v1.1.0-nightly-20260608-5-g1e6a9fc8",
			targetVersion: fallbackNeutreeCoreVersion,
		},
		{
			name:          "rejects target version below v1.1 release line",
			cliVersion:    "v1.1.0-nightly-20260608",
			targetVersion: "v1.0.1",
			wantErr:       "not compatible",
		},
		{
			name:          "rejects target version above v1.1 release line",
			cliVersion:    "v1.1.0-nightly-20260608",
			targetVersion: "v1.2.0",
			wantErr:       "not compatible",
		},
		{
			name:          "rejects previous release line because only current release policy is configured",
			cliVersion:    "v1.0.1-enterprise",
			targetVersion: "v1.0.1-enterprise",
			wantErr:       "no configured",
		},
		{
			name:          "rejects invalid target version",
			cliVersion:    "v1.1.0-nightly-20260608",
			targetVersion: "not-a-version",
			wantErr:       "invalid target version",
		},
		{
			name:          "development CLI still validates target version format",
			cliVersion:    "dev",
			targetVersion: "v1.1.0",
		},
		{
			name:          "development CLI rejects invalid target version",
			cliVersion:    "dev",
			targetVersion: "not-a-version",
			wantErr:       "invalid target version",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateNeutreeCoreVersionCompatibility(tt.cliVersion, tt.targetVersion)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestDefaultNeutreeCoreVersion(t *testing.T) {
	tests := []struct {
		name       string
		cliVersion string
		want       string
	}{
		{
			name:       "exact nightly tag defaults to CLI app version",
			cliVersion: "v1.1.0-nightly-20260610",
			want:       "v1.1.0-nightly-20260610",
		},
		{
			name:       "exact enterprise tag defaults to CLI app version",
			cliVersion: "v1.1.0-enterprise",
			want:       "v1.1.0-enterprise",
		},
		{
			name:       "git describe commit suffix falls back",
			cliVersion: "v1.1.0-nightly-20260608-5-g1e6a9fc8",
			want:       fallbackNeutreeCoreVersion,
		},
		{
			name:       "dirty tag falls back",
			cliVersion: "v1.1.0-nightly-20260608-dirty",
			want:       fallbackNeutreeCoreVersion,
		},
		{
			name:       "dev build falls back",
			cliVersion: "dev",
			want:       fallbackNeutreeCoreVersion,
		},
		{
			name:       "unknown build falls back",
			cliVersion: "unknown",
			want:       fallbackNeutreeCoreVersion,
		},
		{
			name:       "commit-only build falls back",
			cliVersion: "1e6a9fc8",
			want:       fallbackNeutreeCoreVersion,
		},
	}

	oldGetCLIAppVersion := getCLIAppVersion
	t.Cleanup(func() {
		getCLIAppVersion = oldGetCLIAppVersion
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getCLIAppVersion = func() string {
				return tt.cliVersion
			}

			assert.Equal(t, tt.want, defaultNeutreeCoreVersion())
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
				jwtSecret: "test-secret",
				version:   "v1.0.0",
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

func TestInstallNeutreeCoreSingleNodeByDockerRejectsIncompatibleVersionBeforeMutation(t *testing.T) {
	tempDir := t.TempDir()
	composeDir := filepath.Join(tempDir, "neutree-core")
	require.NoError(t, os.MkdirAll(composeDir, 0755))

	composeFile := filepath.Join(composeDir, "docker-compose.yml")
	const sentinel = "sentinel: keep-this-file\n"
	require.NoError(t, os.WriteFile(composeFile, []byte(sentinel), 0600))

	oldGetCLIAppVersion := getCLIAppVersion
	getCLIAppVersion = func() string {
		return "v1.1.0-nightly-20260608"
	}
	t.Cleanup(func() {
		getCLIAppVersion = oldGetCLIAppVersion
	})

	mockExecutor := &mocks.MockExecutor{}
	err := installNeutreeCoreSingleNodeByDocker(mockExecutor, neutreeCoreInstallOptions{
		commonOptions: &commonOptions{
			workDir:    tempDir,
			nodeIP:     "192.168.1.1",
			deployType: constants.DeployTypeLocal,
			deployMode: constants.DeployModeSingle,
		},
		jwtSecret: "test-secret",
		version:   "v1.2.0",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not compatible")
	mockExecutor.AssertNotCalled(t, "Execute", mock.Anything, mock.Anything, mock.Anything)

	got, readErr := os.ReadFile(composeFile)
	require.NoError(t, readErr)
	assert.Equal(t, sentinel, string(got))
}

func TestInstallNeutreeCoreSingleNodeByDockerRejectsEmptyVersionBeforeMutation(t *testing.T) {
	tempDir := t.TempDir()
	composeDir := filepath.Join(tempDir, "neutree-core")
	require.NoError(t, os.MkdirAll(composeDir, 0755))

	composeFile := filepath.Join(composeDir, "docker-compose.yml")
	const sentinel = "sentinel: keep-this-file\n"
	require.NoError(t, os.WriteFile(composeFile, []byte(sentinel), 0600))

	oldGetCLIAppVersion := getCLIAppVersion
	getCLIAppVersion = func() string {
		return "dev"
	}
	t.Cleanup(func() {
		getCLIAppVersion = oldGetCLIAppVersion
	})

	mockExecutor := &mocks.MockExecutor{}
	err := installNeutreeCoreSingleNodeByDocker(mockExecutor, neutreeCoreInstallOptions{
		commonOptions: &commonOptions{
			workDir:    tempDir,
			nodeIP:     "192.168.1.1",
			deployType: constants.DeployTypeLocal,
			deployMode: constants.DeployModeSingle,
		},
		jwtSecret: "test-secret",
		version:   "",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid target version")
	mockExecutor.AssertNotCalled(t, "Execute", mock.Anything, mock.Anything, mock.Anything)

	got, readErr := os.ReadFile(composeFile)
	require.NoError(t, readErr)
	assert.Equal(t, sentinel, string(got))
}

func TestInstallNeutreeCoreSingleNodeByDockerDryRunDoesNotOverwriteExistingCompose(t *testing.T) {
	tempDir := t.TempDir()
	composeDir := filepath.Join(tempDir, "neutree-core")
	require.NoError(t, os.MkdirAll(composeDir, 0755))

	composeFile := filepath.Join(composeDir, "docker-compose.yml")
	const sentinel = "sentinel: keep-this-file\n"
	require.NoError(t, os.WriteFile(composeFile, []byte(sentinel), 0600))

	oldGetCLIAppVersion := getCLIAppVersion
	getCLIAppVersion = func() string {
		return "v1.1.0-nightly-20260608"
	}
	t.Cleanup(func() {
		getCLIAppVersion = oldGetCLIAppVersion
	})

	mockExecutor := &mocks.MockExecutor{}
	var err error
	captureStdout(t, func() {
		err = installNeutreeCoreSingleNodeByDocker(mockExecutor, neutreeCoreInstallOptions{
			commonOptions: &commonOptions{
				workDir:    tempDir,
				nodeIP:     "192.168.1.1",
				deployType: constants.DeployTypeLocal,
				deployMode: constants.DeployModeSingle,
				dryRun:     true,
			},
			jwtSecret: "test-secret",
			version:   "v1.1.0",
		})
	})

	require.NoError(t, err)
	mockExecutor.AssertNotCalled(t, "Execute", mock.Anything, mock.Anything, mock.Anything)

	got, readErr := os.ReadFile(composeFile)
	require.NoError(t, readErr)
	assert.Equal(t, sentinel, string(got))
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	defer reader.Close()

	os.Stdout = writer
	writerClosed := false
	defer func() {
		os.Stdout = oldStdout
		if !writerClosed {
			writer.Close()
		}
	}()

	fn()

	require.NoError(t, writer.Close())
	writerClosed = true
	output, err := io.ReadAll(reader)
	require.NoError(t, err)

	return string(output)
}
