package cluster

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewManager(t *testing.T) {
	clusterName := "test-cluster"
	generator := newRaySSHLocalConfigGenerator(clusterName)

	assert.Contains(t, generator.baseDir, clusterName)
	assert.Contains(t, generator.baseDir, "ray-cluster")
}

func TestConfigPath(t *testing.T) {
	generator := newRaySSHLocalConfigGenerator("test-cluster")
	assert.Contains(t, generator.ConfigPath(), "bootstrap.yaml")
}

func TestSSHKeyPath(t *testing.T) {
	generator := newRaySSHLocalConfigGenerator("test-cluster")
	assert.Contains(t, generator.SSHKeyPath(), "ssh_private_key")
}

func TestGenerate_Success(t *testing.T) {
	tmpDir := t.TempDir()
	generator := &raySSHLocalConfigGenerator{
		baseDir: tmpDir,
	}

	testKey := base64.StdEncoding.EncodeToString([]byte("test-ssh-key"))
	config := &v1.RayClusterConfig{
		Auth: v1.Auth{
			SSHPrivateKey: testKey,
		},
		Provider: v1.Provider{
			WorkerIPs: []string{"1.1.1.1"},
		},
	}

	// Test
	err := generator.Generate(config)
	require.NoError(t, err)

	// Verify files were created
	_, err = os.Stat(generator.ConfigPath())
	assert.NoError(t, err)

	_, err = os.Stat(generator.SSHKeyPath())
	assert.NoError(t, err)

	// Verify SSH key content
	keyData, err := os.ReadFile(generator.SSHKeyPath())
	assert.NoError(t, err)
	assert.Equal(t, "test-ssh-key", string(keyData))
}

func TestGenerate_DecodeSSHKeyError(t *testing.T) {
	tmpDir := t.TempDir()
	generator := &raySSHLocalConfigGenerator{
		baseDir: tmpDir,
	}

	config := &v1.RayClusterConfig{
		Auth: v1.Auth{
			SSHPrivateKey: "invalid-base64",
		},
	}

	err := generator.Generate(config)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "decode ssh key failed")
}

func TestEnsureLocalClusterStateFile(t *testing.T) {
	tests := []struct {
		name          string
		clusterConfig *v1.RayClusterConfig
		setup         func(string, *v1.RayClusterConfig) error
		expectError   bool
	}{
		{
			name: "success with default tmp dir",
			clusterConfig: &v1.RayClusterConfig{
				ClusterName: "test-cluster",
				Provider: v1.Provider{
					HeadIP: "192.168.1.1",
				},
			},
			expectError: false,
		},
		{
			name: "success with custom RAY_TMP_DIR",
			clusterConfig: &v1.RayClusterConfig{
				ClusterName: "test-cluster",
				Provider: v1.Provider{
					HeadIP: "192.168.1.2",
				},
			},
			expectError: false,
		},
		{
			name: "success when state file already exists",
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
			generator := &raySSHLocalConfigGenerator{
				baseDir: tmpDir,
			}

			err := generator.ensureLocalClusterStateFile(tt.clusterConfig)
			if tt.expectError {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)

			stateFilePath := filepath.Join(tmpDir, "ray", "cluster-"+tt.clusterConfig.ClusterName+".state")
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
