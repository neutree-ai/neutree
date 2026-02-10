package framework

import (
	"encoding/base64"
	"net"
	"os"
	"os/user"
	"path/filepath"
)

// Config holds the test configuration loaded from environment variables.
type Config struct {
	APIEndpoint      string // Neutree API endpoint URL
	GatewayURL       string // Kong gateway URL
	AdminEmail       string // Admin user email for authentication
	AdminPassword    string // Admin user password
	HFToken          string // HuggingFace token for model downloads
	Workspace        string // Default workspace to use
	NodeIP           string // Node IP for SSH cluster
	TestModel        string // Model to use for testing
	TestModelFile    string // Model file name (e.g. GGUF file for llama-cpp)
	TestModelVersion string // Model version/revision
	TestEngine       string // Inference engine (llama-cpp, vllm)
	EngineVersion    string // Engine version
	ModelCachePath   string // Host path for model cache
	SSHUser          string // SSH username for cluster
	SSHPrivateKey    string // SSH private key content
	Kubeconfig       string // Kubeconfig content (base64-encoded)
	K8sAccessMode    string // Router access mode: "LoadBalancer" or "NodePort"
}

// NewConfigFromEnv creates a new Config from environment variables.
func NewConfigFromEnv() *Config {
	return &Config{
		APIEndpoint:      getEnv("E2E_API_ENDPOINT", "http://localhost:3000/api/v1"),
		GatewayURL:       getEnv("E2E_GATEWAY_URL", "http://localhost:80"),
		AdminEmail:       getEnv("E2E_ADMIN_EMAIL", "admin@neutree.local"),
		AdminPassword:    getEnv("E2E_ADMIN_PASSWORD", "admin"),
		HFToken:          getEnv("E2E_HF_TOKEN", ""),
		Workspace:        getEnv("E2E_WORKSPACE", "default"),
		NodeIP:           getEnv("E2E_NODE_IP", getLocalIP()),
		TestModel:        getEnv("E2E_TEST_MODEL", "Tinystories-gpt-0.1-3m-GGUF"),
		TestModelFile:    getEnv("E2E_TEST_MODEL_FILE", "*8_0.gguf"),
		TestModelVersion: getEnv("E2E_TEST_MODEL_VERSION", "main"),
		TestEngine:       getEnv("E2E_TEST_ENGINE", "llama-cpp"),
		EngineVersion:    getEnv("E2E_ENGINE_VERSION", "v0.3.7"),
		ModelCachePath:   getEnv("E2E_MODEL_CACHE_PATH", "/tmp/neutree-e2e-model-cache"),
		SSHUser:          getEnv("E2E_SSH_USER", getCurrentUser()),
		SSHPrivateKey:    getEnvOrFile("E2E_SSH_PRIVATE_KEY", "E2E_SSH_PRIVATE_KEY_FILE", getDefaultSSHKeyPath(), true),
		Kubeconfig:       getEnvOrFile("E2E_KUBECONFIG", "E2E_KUBECONFIG_FILE", getDefaultKubeconfigPath(), true),
		K8sAccessMode:    getEnv("E2E_K8S_ACCESS_MODE", "NodePort"),
	}
}

func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

func getEnvOrFile(envKey, fileEnvKey, defaultFilePath string, encode bool) string {
	// First check if direct value is provided (assumed already encoded if needed)
	if v := os.Getenv(envKey); v != "" {
		return v
	}

	// Check if file path is provided
	filePath := os.Getenv(fileEnvKey)
	if filePath == "" {
		filePath = defaultFilePath
	}

	// Read from file
	if filePath != "" {
		data, err := os.ReadFile(filePath)
		if err == nil {
			if encode {
				return base64.StdEncoding.EncodeToString(data)
			}
			return string(data)
		}
	}

	return ""
}

func getLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}

	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}

	return "127.0.0.1"
}

func getCurrentUser() string {
	u, err := user.Current()
	if err != nil {
		return "root"
	}
	return u.Username
}

func getDefaultKubeconfigPath() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	return filepath.Join(u.HomeDir, ".kube", "config")
}

func getDefaultSSHKeyPath() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	return filepath.Join(u.HomeDir, ".ssh", "id_rsa")
}
