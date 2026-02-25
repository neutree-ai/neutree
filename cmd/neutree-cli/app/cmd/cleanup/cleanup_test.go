package cleanup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/neutree-ai/neutree/pkg/command/mocks"
)

func TestNewCleanupCmd(t *testing.T) {
	cmd := NewCleanupCmd()

	assert.Equal(t, "cleanup <neutree-core|obs-stack>", cmd.Use)
	assert.NotNil(t, cmd.RunE)

	removeData, err := cmd.Flags().GetBool("remove-data")
	require.NoError(t, err)
	assert.False(t, removeData)

	force, err := cmd.Flags().GetBool("force")
	require.NoError(t, err)
	assert.False(t, force)
}

func TestRunCleanup(t *testing.T) {
	tests := []struct {
		name       string
		component  string
		removeData bool
		setupMock  func(*mocks.MockExecutor, string)
		wantErr    bool
		errContain string
	}{
		{
			name:       "cleanup neutree-core without removing data",
			component:  "neutree-core",
			removeData: false,
			setupMock: func(m *mocks.MockExecutor, workDir string) {
				composeFile := filepath.Join(workDir, "neutree-core", "docker-compose.yml")
				m.On("Execute", mock.Anything, "docker",
					[]string{"compose", "-p", "neutree-core", "-f", composeFile, "down"},
				).Return([]byte("done"), nil)
			},
		},
		{
			name:       "cleanup obs-stack with remove data",
			component:  "obs-stack",
			removeData: true,
			setupMock: func(m *mocks.MockExecutor, workDir string) {
				composeFile := filepath.Join(workDir, "obs-stack", "docker-compose.yml")
				m.On("Execute", mock.Anything, "docker",
					[]string{"compose", "-p", "obs-stack", "-f", composeFile, "down", "-v"},
				).Return([]byte("done"), nil)
			},
		},
		{
			name:       "docker compose down fails",
			component:  "neutree-core",
			removeData: false,
			setupMock: func(m *mocks.MockExecutor, workDir string) {
				m.On("Execute", mock.Anything, "docker", mock.Anything).
					Return([]byte("error output"), errors.New("exit code 1"))
			},
			wantErr:    true,
			errContain: "failed to run docker compose down",
		},
		{
			name:       "invalid component",
			component:  "invalid",
			removeData: false,
			wantErr:    true,
			errContain: "invalid component",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workDir := t.TempDir()
			t.Setenv("NEUTREE_LAUNCH_WORK_DIR", workDir)

			// Create compose file so the existence check passes (for valid components)
			if validComponents[tt.component] {
				composeDir := filepath.Join(workDir, tt.component)
				require.NoError(t, os.MkdirAll(composeDir, 0750))
				require.NoError(t, os.WriteFile(
					filepath.Join(composeDir, "docker-compose.yml"),
					[]byte("version: '3'"), 0600,
				))
			}

			mockExecutor := &mocks.MockExecutor{}
			if tt.setupMock != nil {
				tt.setupMock(mockExecutor, workDir)
			}

			opts := &cleanupOptions{
				removeData: tt.removeData,
				force:      true,
			}

			err := runCleanup(mockExecutor, opts, tt.component)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContain != "" {
					assert.Contains(t, err.Error(), tt.errContain)
				}
			} else {
				assert.NoError(t, err)
				mockExecutor.AssertExpectations(t)
			}
		})
	}
}

func TestRunCleanupComposeFileNotFound(t *testing.T) {
	workDir := t.TempDir()
	t.Setenv("NEUTREE_LAUNCH_WORK_DIR", workDir)

	mockExecutor := &mocks.MockExecutor{}

	opts := &cleanupOptions{force: true}

	err := runCleanup(mockExecutor, opts, "neutree-core")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "compose file not found")
	assert.Contains(t, err.Error(), "has \"neutree-core\" been launched?")
}
