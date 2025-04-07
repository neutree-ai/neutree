package config

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestNewManager(t *testing.T) {
	clusterName := "test-cluster"
	manager := NewManager(clusterName)

	expectedDir := filepath.Join(os.TempDir(), "ray_cluster", clusterName)
	assert.Equal(t, clusterName, manager.clusterName)
	assert.Equal(t, expectedDir, manager.baseDir)
}

func TestConfigPath(t *testing.T) {
	manager := NewManager("test-cluster")
	expected := filepath.Join(manager.baseDir, "bootstrap.yaml")
	assert.Equal(t, expected, manager.ConfigPath())
}

func TestSSHKeyPath(t *testing.T) {
	manager := NewManager("test-cluster")
	expected := filepath.Join(manager.baseDir, "ssh_private_key")
	assert.Equal(t, expected, manager.SSHKeyPath())
}

func TestGenerate_Success(t *testing.T) {
	// Setup test data
	clusterName := "test-cluster"
	manager := NewManager(clusterName)
	defer os.RemoveAll(manager.baseDir) // Cleanup

	testKey := base64.StdEncoding.EncodeToString([]byte("test-ssh-key"))
	config := &v1.RayClusterConfig{
		Auth: v1.Auth{
			SSHPrivateKey: testKey,
		},
		Provider: v1.Provider{
			WorkerIPs: []string{"1.1.1.1"},
		},
	}

	os.Setenv("TMPDIR", "tmp")
	defer os.Unsetenv("TMPDIR")

	// Test
	err := manager.Generate(config)
	require.NoError(t, err)

	// Verify files were created
	_, err = os.Stat(manager.ConfigPath())
	assert.NoError(t, err)

	_, err = os.Stat(manager.SSHKeyPath())
	assert.NoError(t, err)

	// Verify SSH key content
	keyData, err := os.ReadFile(manager.SSHKeyPath())
	assert.NoError(t, err)
	assert.Equal(t, "test-ssh-key", string(keyData))
}

func TestGenerate_DecodeSSHKeyError(t *testing.T) {
	manager := NewManager("test-cluster")
	defer os.RemoveAll(manager.baseDir) // Cleanup

	config := &v1.RayClusterConfig{
		Auth: v1.Auth{
			SSHPrivateKey: "invalid-base64",
		},
	}

	err := manager.Generate(config)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "decode ssh key failed")
}
