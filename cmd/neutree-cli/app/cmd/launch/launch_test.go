package launch

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/compose-spec/compose-go/cli"
	"github.com/neutree-ai/neutree/pkg/command/mocks"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReplaceComposeImageRegistry(t *testing.T) {
	tempDir := os.TempDir()

	tests := []struct {
		name           string
		composeContent string            // Input docker-compose content
		mirrorRegistry string            // Mirror registry to use
		expectedImages map[string]string // Expected images after replacement
		wantErr        bool              // Whether error is expected
	}{
		{
			name: "empty mirror registry",
			composeContent: `
version: '3.8'
services:
  web:
    image: nginx:latest
`,
			mirrorRegistry: "",
			expectedImages: map[string]string{"web": "nginx:latest"},
			wantErr:        false,
		},
		{
			name: "successful registry replacement",
			composeContent: `
version: '3.8'
services:
  web:
    image: nginx:latest
`,
			mirrorRegistry: "my.registry.com",
			expectedImages: map[string]string{"web": "my.registry.com/library/nginx:latest"},
			wantErr:        false,
		},
		{
			name:           "invalid compose file",
			composeContent: `invalid yaml content`,
			mirrorRegistry: "my.registry.com",
			wantErr:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temp compose file
			composeFile := filepath.Join(tempDir, "docker-compose-test.yml")
			err := os.WriteFile(composeFile, []byte(tt.composeContent), 0644)
			assert.NoError(t, err)
			defer os.Remove(composeFile)

			// Run function
			err = replaceComposeImageRegistry(composeFile, tt.mirrorRegistry)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)

			// Verify images instead of full file content
			project, err := cli.ProjectFromOptions(&cli.ProjectOptions{
				ConfigPaths: []string{composeFile},
			})
			assert.NoError(t, err)

			actualImages := make(map[string]string)
			for _, service := range project.Services {
				if service.Image != "" {
					actualImages[service.Name] = service.Image
				}
			}

			assert.Equal(t, tt.expectedImages, actualImages)
		})
	}
}

func TestNewLaunchCmd(t *testing.T) {
	// Setup test cases
	tests := []struct {
		name        string
		setup       func(*mocks.MockExecutor)
		envWorkDir  string
		expectedCmd func(*testing.T, *cobra.Command)
	}{
		{
			name: "successful command creation with default options",
			setup: func(mock *mocks.MockExecutor) {
				// No expectations needed for command creation
			},
			envWorkDir: "",
			expectedCmd: func(t *testing.T, cmd *cobra.Command) {
				assert.Equal(t, "launch", cmd.Use)
				assert.Equal(t, "Deploy Neutree components", cmd.Short)
				assert.Contains(t, cmd.Long, "The launch command provides an easy way to install")

				// Verify flags
				deployType, err := cmd.PersistentFlags().GetString("deploy-type")
				require.NoError(t, err)
				assert.Equal(t, "local", deployType)

				deployMode, err := cmd.PersistentFlags().GetString("deploy-mode")
				require.NoError(t, err)
				assert.Equal(t, "single", deployMode)

				deployIps, err := cmd.PersistentFlags().GetStringSlice("deploy-ips")
				require.NoError(t, err)
				assert.Empty(t, deployIps)

				mirrorRegistry, err := cmd.PersistentFlags().GetString("mirror-registry")
				require.NoError(t, err)
				assert.Empty(t, mirrorRegistry)

				// Verify subcommands
				assert.NotNil(t, cmd.Commands())
				assert.Equal(t, 2, len(cmd.Commands()))
			},
		},
		{
			name: "successful command creation with custom work dir",
			setup: func(mock *mocks.MockExecutor) {
				// No expectations needed for command creation
			},
			envWorkDir: filepath.Join(os.TempDir(), "custom-neutree-workdir"),
			expectedCmd: func(t *testing.T, cmd *cobra.Command) {
				assert.Equal(t, "launch", cmd.Use)
				assert.Equal(t, "Deploy Neutree components", cmd.Short)

				// Verify flags
				deployType, err := cmd.PersistentFlags().GetString("deploy-type")
				require.NoError(t, err)
				assert.Equal(t, "local", deployType)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup environment if needed
			if tt.envWorkDir != "" {
				os.Setenv("NEUTREE_LAUNCH_WORK_DIR", tt.envWorkDir)
				defer os.Unsetenv("NEUTREE_LAUNCH_WORK_DIR")
			}

			// Create mock executor
			mockExecutor := &mocks.MockExecutor{}
			if tt.setup != nil {
				tt.setup(mockExecutor)
			}

			// Create command
			cmd := NewLaunchCmd()

			// Verify command properties
			tt.expectedCmd(t, cmd)
		})
	}
}
