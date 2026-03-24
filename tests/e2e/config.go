package e2e

import (
	"bufio"
	"crypto/rand"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
)

// E2EConfig holds all configuration for e2e tests.
// Values are loaded once at package init from environment variables,
// with optional .env file support.
type E2EConfig struct {
	// --- Global ---
	ServerURL string // NEUTREE_SERVER_URL (required)
	APIKey    string // NEUTREE_API_KEY (required)
	Workspace string // E2E_WORKSPACE (default: "default")

	// --- Run ID ---
	RunID string // auto-generated 6-digit random suffix for resource name uniqueness

	// --- Image Registry ---
	ImageRegistryURL  string // E2E_IMAGE_REGISTRY_URL
	ImageRegistryRepo string // E2E_IMAGE_REGISTRY_REPO
	ImageRegistryName string // E2E_IMAGE_REGISTRY (default: "e2e-image-registry-" + RunID)

	// --- SSH Cluster ---
	SSHHeadIP     string // E2E_SSH_HEAD_IP
	SSHWorkerIPs  string // E2E_SSH_WORKER_IPS
	SSHUser       string // E2E_SSH_USER (default: "root")
	SSHPrivateKey string // E2E_SSH_PRIVATE_KEY

	// --- K8s Cluster ---
	Kubeconfig string // E2E_KUBECONFIG

	// --- Cluster ---
	ClusterVersion        string // E2E_CLUSTER_VERSION (default: "v1.0.0")
	ClusterUpgradeVersion string // E2E_CLUSTER_UPGRADE_VERSION
	ClusterName           string // E2E_CLUSTER_NAME

	// --- Model ---
	ModelRegistryURL  string // E2E_MODEL_REGISTRY_URL
	ModelRegistryName string // E2E_MODEL_REGISTRY (default: "e2e-registry-" + RunID)
	ModelName         string // E2E_MODEL_NAME
	ModelVersion      string // E2E_MODEL_VERSION (default: "")
	ModelTask         string // E2E_MODEL_TASK (default: "text-generation")

	// --- Engine / Endpoint ---
	EngineName         string // E2E_ENGINE_NAME (default: "vllm")
	EngineVersionA     string // E2E_ENGINE_VERSION_A (default: "v0.8.5")
	EngineVersionB     string // E2E_ENGINE_VERSION_B (default: "v0.11.2")
	AcceleratorType    string // E2E_ACCELERATOR_TYPE (default: "nvidia_gpu")
	AcceleratorProduct string // E2E_ACCELERATOR_PRODUCT (default: "")
	EndpointEngineArgs string // E2E_ENDPOINT_ENGINE_ARGS (default: "dtype=half")
	EndpointTimeout    string // E2E_ENDPOINT_TIMEOUT (default: "10m")

	// --- Mock Upstream ---
	MockUpstreamHost string // E2E_MOCK_UPSTREAM_HOST (default: "host.docker.internal")

	// --- TestRail ---
	TestRailRunID    string // TESTRAIL_RUN_ID
	TestRailURL      string // TESTRAIL_URL
	TestRailUser     string // TESTRAIL_USER
	TestRailPassword string // TESTRAIL_PASSWORD
}

// Cfg is the global e2e configuration, loaded once at package init.
var Cfg = loadConfig()

func loadConfig() *E2EConfig {
	// Try to load .env file from test directory
	loadDotEnv()

	id := generateRunID()

	return &E2EConfig{
		// Global
		ServerURL: os.Getenv("NEUTREE_SERVER_URL"),
		APIKey:    os.Getenv("NEUTREE_API_KEY"),
		Workspace: envOr("E2E_WORKSPACE", "default"),

		RunID: id,

		// Image Registry
		ImageRegistryURL:  os.Getenv("E2E_IMAGE_REGISTRY_URL"),
		ImageRegistryRepo: os.Getenv("E2E_IMAGE_REGISTRY_REPO"),
		ImageRegistryName: envOr("E2E_IMAGE_REGISTRY", "e2e-image-registry-"+id),

		// SSH Cluster
		SSHHeadIP:     os.Getenv("E2E_SSH_HEAD_IP"),
		SSHWorkerIPs:  os.Getenv("E2E_SSH_WORKER_IPS"),
		SSHUser:       envOr("E2E_SSH_USER", "root"),
		SSHPrivateKey: os.Getenv("E2E_SSH_PRIVATE_KEY"),

		// K8s Cluster
		Kubeconfig: os.Getenv("E2E_KUBECONFIG"),

		// Cluster
		ClusterVersion:        envOr("E2E_CLUSTER_VERSION", "v1.0.0"),
		ClusterUpgradeVersion: os.Getenv("E2E_CLUSTER_UPGRADE_VERSION"),
		ClusterName:           os.Getenv("E2E_CLUSTER_NAME"),

		// Model
		ModelRegistryURL:  os.Getenv("E2E_MODEL_REGISTRY_URL"),
		ModelRegistryName: envOr("E2E_MODEL_REGISTRY", "e2e-registry-"+id),
		ModelName:         os.Getenv("E2E_MODEL_NAME"),
		ModelVersion:      envOr("E2E_MODEL_VERSION", ""),
		ModelTask:         envOr("E2E_MODEL_TASK", "text-generation"),

		// Engine / Endpoint
		EngineName:         envOr("E2E_ENGINE_NAME", "vllm"),
		EngineVersionA:     envOr("E2E_ENGINE_VERSION_A", "v0.8.5"),
		EngineVersionB:     envOr("E2E_ENGINE_VERSION_B", "v0.11.2"),
		AcceleratorType:    envOr("E2E_ACCELERATOR_TYPE", "nvidia_gpu"),
		AcceleratorProduct: envOr("E2E_ACCELERATOR_PRODUCT", ""),
		EndpointEngineArgs: envOr("E2E_ENDPOINT_ENGINE_ARGS", "dtype=half"),
		EndpointTimeout:    envOr("E2E_ENDPOINT_TIMEOUT", "10m"),

		// Mock Upstream
		MockUpstreamHost: envOr("E2E_MOCK_UPSTREAM_HOST", "host.docker.internal"),

		// TestRail
		TestRailRunID:    os.Getenv("TESTRAIL_RUN_ID"),
		TestRailURL:      os.Getenv("TESTRAIL_URL"),
		TestRailUser:     os.Getenv("TESTRAIL_USER"),
		TestRailPassword: os.Getenv("TESTRAIL_PASSWORD"),
	}
}

// envOr returns the environment variable value, or the fallback if empty.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return fallback
}

func generateRunID() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(1000000))
	return fmt.Sprintf("%06d", n.Int64())
}

// loadDotEnv loads a .env file if present. It does NOT override existing env vars.
func loadDotEnv() {
	// Look for .env in the test directory (where go test runs)
	paths := []string{".env", filepath.Join("tests", "e2e", ".env")}

	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		defer f.Close()

		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}

			key, value, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}

			key = strings.TrimSpace(key)
			value = strings.TrimSpace(value)

			// Don't override existing env vars
			if os.Getenv(key) == "" {
				os.Setenv(key, value)
			}
		}

		return // loaded one file, done
	}
}
