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

	Workspace string `yaml:"workspace"`

	Cluster struct {
		Version        string `yaml:"version"`
		UpgradeVersion string `yaml:"upgrade_version"`
	} `yaml:"cluster"`

	Engine struct {
		Name     string `yaml:"name"`
		Version  string `yaml:"version"`
		VersionB string `yaml:"version_b"`
	} `yaml:"engine"`

	Model struct {
		Name    string `yaml:"name"`
		Version string `yaml:"version"`
		File    string `yaml:"file"`
		Task    string `yaml:"task"`
	} `yaml:"model"`

	Endpoint struct {
		Cluster            string `yaml:"cluster"`
		AcceleratorType    string `yaml:"accelerator_type"`
		AcceleratorProduct string `yaml:"accelerator_product"`
		EngineArgs         string `yaml:"engine_args"`
		Timeout            string `yaml:"timeout"`
	} `yaml:"endpoint"`

	MockUpstreamHost string `yaml:"mock_upstream_host"`

	ModelCache struct {
		HostPath        string `yaml:"host_path"`
		NFSServer       string `yaml:"nfs_server"`
		NFSPath         string `yaml:"nfs_path"`
		PVCStorageClass string `yaml:"pvc_storage_class"`
	} `yaml:"model_cache"`

	// Computed fields (populated by LoadProfile, not from YAML directly)
	sshHeadPrivateKeyBase64 string // base64-encoded content of SSHNodes[0].KeyFile
	sshWorkerIPs            string // comma-separated worker IPs
	kubeconfigBase64        string // base64-encoded content of Kubernetes.Kubeconfig
}

// profile is the package-level profile instance, populated by LoadProfile().
var profile Profile

// LoadProfile loads a profile from E2E_PROFILE_PATH and populates the
// package-level profile variable. If E2E_PROFILE_PATH is not set, the
// profile remains zero-valued (tests use defaults).
func LoadProfile() error {
	path := os.Getenv("E2E_PROFILE_PATH")
	if path == "" {
		return nil // no profile, use defaults
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read profile %s: %w", path, err)
	}

	if err := yaml.Unmarshal(data, &profile); err != nil {
		return fmt.Errorf("failed to parse profile %s: %w", path, err)
	}

	// Compute derived fields.

	// SSH: read key file and base64-encode; compute worker IPs.
	if len(profile.SSHNodes) > 0 {
		head := profile.SSHNodes[0]
		if head.KeyFile != "" {
			keyFile := expandHome(head.KeyFile)

			keyData, err := os.ReadFile(keyFile)
			if err != nil {
				return fmt.Errorf("failed to read SSH key file %s: %w", keyFile, err)
			}

			profile.sshHeadPrivateKeyBase64 = base64.StdEncoding.EncodeToString(keyData)
		}

		if len(profile.SSHNodes) > 1 {
			var workerIPs []string
			for _, n := range profile.SSHNodes[1:] {
				workerIPs = append(workerIPs, n.Host)
			}

			profile.sshWorkerIPs = strings.Join(workerIPs, ",")
		}
	}

	// Kubernetes: read kubeconfig file and base64-encode.
	if profile.Kubernetes.Kubeconfig != "" {
		kcPath := expandHome(profile.Kubernetes.Kubeconfig)

		kcData, err := os.ReadFile(kcPath)
		if err != nil {
			return fmt.Errorf("failed to read kubeconfig %s: %w", kcPath, err)
		}

		profile.kubeconfigBase64 = base64.StdEncoding.EncodeToString(kcData)
	}

	return nil
}

// --- Profile accessor helpers ---

// profileSSHHeadIP returns the SSH head node IP, or empty string if not configured.
func profileSSHHeadIP() string {
	if len(profile.SSHNodes) > 0 {
		return profile.SSHNodes[0].Host
	}

	return ""
}

// profileSSHUser returns the SSH user, or empty string if not configured.
func profileSSHUser() string {
	if len(profile.SSHNodes) > 0 {
		return profile.SSHNodes[0].User
	}

	return ""
}

// profileSSHPrivateKey returns the base64-encoded SSH private key.
func profileSSHPrivateKey() string {
	return profile.sshHeadPrivateKeyBase64
}


// profileSSHWorkerIPs returns comma-separated worker IPs.
func profileSSHWorkerIPs() string {
	return profile.sshWorkerIPs
}

// profileKubeconfig returns the base64-encoded kubeconfig.
func profileKubeconfig() string {
	return profile.kubeconfigBase64
}

// profileTestrailRunID returns the TestRail run ID.
// TESTRAIL_RUN_ID env var (from test-infra) takes precedence, then profile value.
func profileTestrailRunID() string {
	if v := os.Getenv("TESTRAIL_RUN_ID"); v != "" {
		return v
	}

	if profile.Testrail.RunID != nil {
		return fmt.Sprintf("%v", profile.Testrail.RunID)
	}

	return ""
}

// --- Workspace / Cluster / Engine / Model / Endpoint accessors ---

func profileWorkspace() string {
	if profile.Workspace != "" {
		return profile.Workspace
	}

	return "default"
}

func profileClusterVersion() string {
	if profile.Cluster.Version != "" {
		return profile.Cluster.Version
	}

	return "v1.0.0"
}

func profileClusterUpgradeVersion() string { return profile.Cluster.UpgradeVersion }

func profileEngineName() string {
	if profile.Engine.Name != "" {
		return profile.Engine.Name
	}

	return "vllm"
}

func profileEngineVersion() string {
	if profile.Engine.Version != "" {
		return profile.Engine.Version
	}

	return "v0.8.5"
}

func profileEngineVersionB() string {
	if profile.Engine.VersionB != "" {
		return profile.Engine.VersionB
	}

	return "v0.11.2"
}

func profileModelName() string { return profile.Model.Name }

func profileModelVersion() string {
	if profile.Model.Version != "" {
		return profile.Model.Version
	}

	return "latest"
}

func profileModelTask() string {
	if profile.Model.Task != "" {
		return profile.Model.Task
	}

	return "text-generation"
}

func profileEndpointCluster() string { return profile.Endpoint.Cluster }

func profileAcceleratorType() string {
	if profile.Endpoint.AcceleratorType != "" {
		return profile.Endpoint.AcceleratorType
	}

	return "nvidia_gpu"
}

func profileAcceleratorProduct() string { return profile.Endpoint.AcceleratorProduct }

func profileEngineArgs() string {
	if profile.Endpoint.EngineArgs != "" {
		return profile.Endpoint.EngineArgs
	}

	return "dtype=half"
}

func profileEndpointTimeout() string {
	if profile.Endpoint.Timeout != "" {
		return profile.Endpoint.Timeout
	}

	return "10m"
}

func profileMockUpstreamHost() string {
	if profile.MockUpstreamHost != "" {
		return profile.MockUpstreamHost
	}

	return "host.docker.internal"
}

// expandHome replaces leading ~ with $HOME.
func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return home + path[1:]
	}

	return path
}
