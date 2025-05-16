package config

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type Manager struct {
	clusterName string
	baseDir     string
}

func NewManager(clusterName string) *Manager {
	return &Manager{
		clusterName: clusterName,
		baseDir:     filepath.Join(os.TempDir(), "ray_cluster", clusterName),
	}
}

func (m *Manager) Generate(config *v1.RayClusterConfig) error {
	if err := os.MkdirAll(m.baseDir, 0700); err != nil {
		return errors.Wrap(err, "create config dir failed")
	}

	// generate SSH key
	sshKeyPath := m.SSHKeyPath()
	if _, err := os.Stat(sshKeyPath); err != nil {
		decodedKey, err := base64.StdEncoding.DecodeString(config.Auth.SSHPrivateKey)
		if err != nil {
			return errors.Wrap(err, "decode ssh key failed")
		}

		if err = os.WriteFile(sshKeyPath, decodedKey, 0600); err != nil {
			return errors.Wrap(err, "write ssh key failed")
		}
	}

	configPath := m.ConfigPath()
	if _, err := os.Stat(configPath); err != nil {
		// deep copy by json marshal/unmarshal
		curConfigContent, err := json.Marshal(config)
		if err != nil {
			return err
		}

		configCopy := &v1.RayClusterConfig{}
		if err = json.Unmarshal(curConfigContent, configCopy); err != nil {
			return err
		}

		configCopy.Provider.WorkerIPs = nil
		configCopy.Auth.SSHPrivateKey = sshKeyPath

		configData, err := yaml.Marshal(configCopy)
		if err != nil {
			return errors.Wrap(err, "marshal config failed")
		}

		err = os.WriteFile(configPath, configData, 0600)
		if err != nil {
			return errors.Wrap(err, "write config failed")
		}
	}

	return nil
}

func (m *Manager) ConfigPath() string {
	return filepath.Join(m.baseDir, "bootstrap.yaml")
}

func (m *Manager) SSHKeyPath() string {
	return filepath.Join(m.baseDir, "ssh_private_key")
}
