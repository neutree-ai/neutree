package e2e

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

const defaultModelVersion = "latest"

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
		Version    string `yaml:"version"`
		OldVersion string `yaml:"old_version"` // for upgrade tests: the version to create before upgrading
	} `yaml:"cluster"`

	Engine struct {
		Name       string `yaml:"name"`
		Version    string `yaml:"version"`
		OldVersion string `yaml:"old_version"` // for multi-version isolation tests only
	} `yaml:"engine"`

	Model struct {
		Name    string `yaml:"name"`
		Version string `yaml:"version"`
		File    string `yaml:"file"`
		Task    string `yaml:"task"`
	} `yaml:"model"`

	EmbeddingModel struct {
		Name    string `yaml:"name"`
		Version string `yaml:"version"`
	} `yaml:"embedding_model"`

	RerankModel struct {
		Name    string `yaml:"name"`
		Version string `yaml:"version"`
	} `yaml:"rerank_model"`

	Endpoint struct {
		EngineArgs string `yaml:"engine_args"`
		Timeout    string `yaml:"timeout"`
	} `yaml:"endpoint"`

	MockUpstreamHost string `yaml:"mock_upstream_host"`

	ModelCache struct {
		HostPath        string `yaml:"host_path"`
		NFSServer       string `yaml:"nfs_server"`
		NFSPath         string `yaml:"nfs_path"`
		PVCStorageClass string `yaml:"pvc_storage_class"`
	} `yaml:"model_cache"`

	ControlPlane struct {
		DeployMode      string `yaml:"deploy_mode"`       // docker-compose | kubernetes
		Host            string `yaml:"host"`              // VM IP (docker-compose mode)
		SSHUser         string `yaml:"ssh_user"`          // SSH user (docker-compose mode)
		SSHKey          string `yaml:"ssh_key"`           // path to SSH private key (docker-compose mode)
		ComposeDir      string `yaml:"compose_dir"`       // neutree-cli working directory on VM (docker-compose mode)
		Kubeconfig      string `yaml:"kubeconfig"`        // kubeconfig file path (kubernetes mode)
		K8sNamespace    string `yaml:"k8s_namespace"`     // helm install namespace (kubernetes mode, default: neutree-e2e)
		APIPort         int    `yaml:"api_port"`          // neutree-api port (default 3000)
		Version         string `yaml:"version"`           // CP version for launch --version (e.g. v1.0.1)
		CLIURL          string `yaml:"cli_url"`           // URL to download CLI binary (enterprise); if empty, compile from source
		ChartURL        string `yaml:"chart_url"`         // URL to download helm chart .tgz (enterprise); if empty, use local chart
		MirrorRegistry  string `yaml:"mirror_registry"`   // mirror registry for launch --mirror-registry
		RegistryProject string `yaml:"registry_project"`  // registry project for launch --registry-project
		OfflineImageURL string `yaml:"offline_image_url"` // URL to download offline image archive
		OldVersion      string `yaml:"old_version"`       // old CP version for upgrade tests (e.g. v1.0.0)
		OldCLIURL       string `yaml:"old_cli_url"`       // URL to download old version CLI binary
	} `yaml:"control_plane"`

	// Computed fields (populated by LoadProfile, not from YAML directly)
	sshHeadPrivateKeyBase64 string // base64-encoded content of SSHNodes[0].KeyFile
	sshWorkerIPs            string // comma-separated worker IPs
	kubeconfigBase64        string // base64-encoded content of Kubernetes.Kubeconfig
	cpSSHKeyPath            string // expanded path to control plane SSH key
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

	// Control plane: expand paths and set defaults.
	if profile.ControlPlane.SSHKey != "" {
		profile.cpSSHKeyPath = expandHome(profile.ControlPlane.SSHKey)
	}

	if profile.ControlPlane.Kubeconfig != "" {
		profile.ControlPlane.Kubeconfig = expandHome(profile.ControlPlane.Kubeconfig)
	}

	if profile.ControlPlane.APIPort == 0 {
		profile.ControlPlane.APIPort = 3000
	}

	if profile.ControlPlane.K8sNamespace == "" {
		profile.ControlPlane.K8sNamespace = "neutree-e2e"
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

	return "v1.0.1"
}

func profileEngineName() string {
	if profile.Engine.Name != "" {
		return profile.Engine.Name
	}

	return "vllm"
}

// profileEngineVersion returns the primary engine version (default: v0.11.2).
// Used by all endpoint deployments.
func profileEngineVersion() string {
	if profile.Engine.Version != "" {
		return profile.Engine.Version
	}

	return "v0.11.2"
}

// profileEngineOldVersion returns the old engine version for multi-version isolation tests (default: v0.8.5).
func profileEngineOldVersion() string {
	if profile.Engine.OldVersion != "" {
		return profile.Engine.OldVersion
	}

	return "v0.8.5"
}

func profileModelName() string { return profile.Model.Name }

func profileModelVersion() string {
	if profile.Model.Version != "" {
		return profile.Model.Version
	}

	return defaultModelVersion
}

func profileModelTask() string {
	if profile.Model.Task != "" {
		return profile.Model.Task
	}

	return "text-generation"
}

func profileEngineArgs() string {
	if profile.Endpoint.EngineArgs != "" {
		return profile.Endpoint.EngineArgs
	}

	return "dtype=half,gpu_memory_utilization=0.85"
}

func profileEndpointTimeout() string {
	if profile.Endpoint.Timeout != "" {
		return profile.Endpoint.Timeout
	}

	return "10m"
}

func profileEmbeddingModelName() string { return profile.EmbeddingModel.Name }
func profileEmbeddingModelVersion() string {
	if profile.EmbeddingModel.Version != "" {
		return profile.EmbeddingModel.Version
	}

	return defaultModelVersion
}

func profileRerankModelName() string { return profile.RerankModel.Name }
func profileRerankModelVersion() string {
	if profile.RerankModel.Version != "" {
		return profile.RerankModel.Version
	}

	return defaultModelVersion
}

func profileMockUpstreamHost() string {
	if profile.MockUpstreamHost != "" {
		return profile.MockUpstreamHost
	}

	return "host.docker.internal"
}

// --- Control Plane accessors ---

func profileCPHost() string            { return profile.ControlPlane.Host }
func profileCPSSHUser() string         { return profile.ControlPlane.SSHUser }
func profileCPSSHKeyPath() string      { return profile.cpSSHKeyPath }
func profileCPComposeDir() string      { return profile.ControlPlane.ComposeDir }
func profileCPAPIPort() int            { return profile.ControlPlane.APIPort }
func profileCPVersion() string         { return profile.ControlPlane.Version }
func profileCPCLIURL() string          { return profile.ControlPlane.CLIURL }
func profileCPChartURL() string        { return profile.ControlPlane.ChartURL }
func profileCPMirrorRegistry() string  { return profile.ControlPlane.MirrorRegistry }
func profileCPRegistryProject() string { return profile.ControlPlane.RegistryProject }
func profileCPOfflineImageURL() string { return profile.ControlPlane.OfflineImageURL }
func profileCPKubeconfig() string      { return profile.ControlPlane.Kubeconfig }
func profileCPK8sNamespace() string    { return profile.ControlPlane.K8sNamespace }
func profileCPOldVersion() string      { return profile.ControlPlane.OldVersion }
func profileCPOldCLIURL() string       { return profile.ControlPlane.OldCLIURL }

// expandHome replaces leading ~ with $HOME.
func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return home + path[1:]
	}

	return path
}
