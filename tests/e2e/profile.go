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
			if keyData, err := os.ReadFile(keyFile); err == nil {
				profile.sshHeadPrivateKeyBase64 = base64.StdEncoding.EncodeToString(keyData)
			}
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
		if kcData, err := os.ReadFile(kcPath); err == nil {
			profile.kubeconfigBase64 = base64.StdEncoding.EncodeToString(kcData)
		}
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

// expandHome replaces leading ~ with $HOME.
func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return home + path[1:]
	}

	return path
}
