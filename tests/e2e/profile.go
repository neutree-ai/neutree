package e2e

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Profile represents the test-infra standard profile format (snake_case).
type Profile struct {
	Auth struct {
		Email    string `yaml:"email"`
		Password string `yaml:"password"`
	} `yaml:"auth"`

	Testrail struct {
		RunID    interface{} `yaml:"run_id"` // can be string or int
		URL      string      `yaml:"url"`
		User     string      `yaml:"user"`
		Password string      `yaml:"password"`
	} `yaml:"testrail"`

	SSHNodes []struct {
		Host    string `yaml:"host"`
		User    string `yaml:"user"`
		KeyFile string `yaml:"key_file"`
	} `yaml:"ssh_nodes"`

	Kubernetes struct {
		Kubeconfig       string `yaml:"kubeconfig"`
		RouterAccessMode string `yaml:"router_access_mode"`
	} `yaml:"kubernetes"`

	ImageRegistry struct {
		URL        string `yaml:"url"`
		Repository string `yaml:"repository"`
		Username   string `yaml:"username"`
		Password   string `yaml:"password"`
	} `yaml:"image_registry"`

	ModelRegistry struct {
		Type        string `yaml:"type"`
		URL         string `yaml:"url"`
		Credentials string `yaml:"credentials"`
	} `yaml:"model_registry"`

	Engine struct {
		Name    string `yaml:"name"`
		Version string `yaml:"version"`
	} `yaml:"engine"`

	Model struct {
		Name    string `yaml:"name"`
		Version string `yaml:"version"`
		File    string `yaml:"file"`
		Task    string `yaml:"task"`
	} `yaml:"model"`

	ModelCache struct {
		HostPath        string `yaml:"host_path"`
		NFSServer       string `yaml:"nfs_server"`
		NFSPath         string `yaml:"nfs_path"`
		PVCStorageClass string `yaml:"pvc_storage_class"`
	} `yaml:"model_cache"`
}

// LoadProfileFromEnv loads a profile from E2E_PROFILE_PATH and sets
// environment variables that tests expect. Only sets vars that are
// not already set (env vars take precedence).
func LoadProfileFromEnv() error {
	path := os.Getenv("E2E_PROFILE_PATH")
	if path == "" {
		return nil // no profile, rely on env vars
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read profile %s: %w", path, err)
	}

	var p Profile
	if err := yaml.Unmarshal(data, &p); err != nil {
		return fmt.Errorf("failed to parse profile %s: %w", path, err)
	}

	// SSH nodes → E2E_SSH_*
	if len(p.SSHNodes) > 0 {
		head := p.SSHNodes[0]
		setDefault("E2E_SSH_HEAD_IP", head.Host)
		setDefault("E2E_SSH_USER", head.User)

		// Read key file and base64 encode
		if head.KeyFile != "" {
			keyFile := expandHome(head.KeyFile)
			if keyData, err := os.ReadFile(keyFile); err == nil {
				setDefault("E2E_SSH_PRIVATE_KEY", base64.StdEncoding.EncodeToString(keyData))
			}
		}

		// Worker IPs
		if len(p.SSHNodes) > 1 {
			var workerIPs []string
			for _, n := range p.SSHNodes[1:] {
				workerIPs = append(workerIPs, n.Host)
			}
			setDefault("E2E_SSH_WORKER_IPS", strings.Join(workerIPs, ","))
		}
	}

	// Kubernetes → E2E_KUBECONFIG (base64)
	if p.Kubernetes.Kubeconfig != "" {
		kcPath := expandHome(p.Kubernetes.Kubeconfig)
		if kcData, err := os.ReadFile(kcPath); err == nil {
			setDefault("E2E_KUBECONFIG", base64.StdEncoding.EncodeToString(kcData))
		}
	}

	// Image registry
	setDefault("E2E_IMAGE_REGISTRY_URL", p.ImageRegistry.URL)
	setDefault("E2E_IMAGE_REGISTRY_REPO", p.ImageRegistry.Repository)

	// Model registry
	setDefault("E2E_MODEL_REGISTRY_URL", p.ModelRegistry.URL)

	// Engine
	setDefault("E2E_ENGINE_NAME", p.Engine.Name)
	setDefault("E2E_ENGINE_VERSION_A", p.Engine.Version)

	// Model
	setDefault("E2E_MODEL_NAME", p.Model.Name)
	setDefault("E2E_MODEL_VERSION", p.Model.Version)
	setDefault("E2E_MODEL_TASK", p.Model.Task)

	// TestRail
	if p.Testrail.RunID != nil {
		setDefault("TESTRAIL_RUN_ID", fmt.Sprintf("%v", p.Testrail.RunID))
	}
	setDefault("TESTRAIL_URL", p.Testrail.URL)
	setDefault("TESTRAIL_USER", p.Testrail.User)
	setDefault("TESTRAIL_PASSWORD", p.Testrail.Password)

	return nil
}

// setDefault sets an environment variable only if it's not already set.
func setDefault(key, value string) {
	if value == "" {
		return
	}
	if os.Getenv(key) == "" {
		os.Setenv(key, value)
	}
}

// expandHome replaces leading ~ with $HOME.
func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return home + path[1:]
	}
	return path
}
