package cluster

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type raySSHLocalConfigGenerator struct {
	baseDir string
}

func newRaySSHLocalConfigGenerator(clusterName string) *raySSHLocalConfigGenerator {
	return &raySSHLocalConfigGenerator{
		baseDir: filepath.Join(os.TempDir(), "ray-cluster-"+uuid.New().String()[0:6], clusterName),
	}
}

func (m *raySSHLocalConfigGenerator) Generate(config *v1.RayClusterConfig) error {
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

	err := m.ensureLocalClusterStateFile(config)
	if err != nil {
		return errors.Wrap(err, "ensure local cluster state file failed")
	}

	return nil
}

func (m *raySSHLocalConfigGenerator) BasePath() string {
	return m.baseDir
}

func (m *raySSHLocalConfigGenerator) ConfigPath() string {
	return filepath.Join(m.baseDir, "bootstrap.yaml")
}

func (m *raySSHLocalConfigGenerator) SSHKeyPath() string {
	return filepath.Join(m.baseDir, "ssh_private_key")
}

func (m *raySSHLocalConfigGenerator) Cleanup() error {
	parentDir := filepath.Dir(m.baseDir)
	return os.RemoveAll(parentDir)
}

func (m *raySSHLocalConfigGenerator) ensureLocalClusterStateFile(config *v1.RayClusterConfig) error {
	parentDir := filepath.Join(m.baseDir, "ray")
	if err := os.MkdirAll(parentDir, 0700); err != nil {
		return errors.Wrap(err, "create ray dir failed")
	}

	localClusterStateFilePath := filepath.Join(parentDir, "cluster-"+config.ClusterName+".state")
	if _, err := os.Stat(localClusterStateFilePath); err == nil {
		return nil
	}

	// create local cluster state file
	localClusterState := map[string]v1.LocalNodeStatus{}
	localClusterState[config.Provider.HeadIP] = v1.LocalNodeStatus{
		Tags: map[string]string{
			"ray-node-type":   "head",
			"ray-node-status": "up-to-date",
		},
		State: "running",
	}

	localClusterStateContent, err := json.Marshal(localClusterState)
	if err != nil {
		return err
	}

	err = os.WriteFile(localClusterStateFilePath, localClusterStateContent, 0600)
	if err != nil {
		return err
	}

	return nil
}
