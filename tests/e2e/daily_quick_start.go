package e2e

import (
	"fmt"
	"os"
	"strings"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/semver"
)

const (
	dailyTargetAMD64K8s      = "amd64-k8s"
	dailyTargetARM64K8s      = "arm64-k8s"
	dailyTargetAMD64VM       = "amd64-vm"
	dailyTargetAMD64L20K8s   = "amd64-l20-k8s"
	dailyTargetAMD64T4Static = "amd64-t4-static"

	envDailyTarget                     = "E2E_DAILY_TARGET"
	envDailyRuntimeRef                 = "E2E_RUNTIME_REF"
	envDailyExpectedAcceleratorProduct = "E2E_DAILY_EXPECTED_ACCELERATOR_PRODUCT"
)

type dailyQuickStartConfig struct {
	Target                     string
	ProfilePath                string
	ServerURL                  string
	APIKey                     string
	RuntimeRef                 string
	ExpectedAcceleratorProduct string
	TestRailRunID              string
}

func isDailyQuickStartRun() bool {
	return strings.TrimSpace(os.Getenv(envDailyTarget)) != ""
}

func dailyQuickStartTarget() string {
	return strings.TrimSpace(os.Getenv(envDailyTarget))
}

func validateDailyQuickStartFromEnv() error {
	if !isDailyQuickStartRun() {
		return nil
	}

	return validateDailyQuickStart(profile, dailyQuickStartConfig{
		Target:                     dailyQuickStartTarget(),
		ProfilePath:                strings.TrimSpace(os.Getenv("E2E_PROFILE_PATH")),
		ServerURL:                  strings.TrimSpace(os.Getenv("NEUTREE_SERVER_URL")),
		APIKey:                     strings.TrimSpace(os.Getenv("NEUTREE_API_KEY")),
		RuntimeRef:                 strings.TrimSpace(os.Getenv(envDailyRuntimeRef)),
		ExpectedAcceleratorProduct: strings.TrimSpace(os.Getenv(envDailyExpectedAcceleratorProduct)),
		TestRailRunID:              strings.TrimSpace(os.Getenv("TESTRAIL_RUN_ID")),
	})
}

func validateDailyQuickStart(profile Profile, cfg dailyQuickStartConfig) error {
	target := strings.TrimSpace(cfg.Target)
	if !isDailyQuickStartTarget(target) {
		return fmt.Errorf("unsupported E2E_DAILY_TARGET %q", cfg.Target)
	}

	if cfg.ServerURL == "" {
		return fmt.Errorf("NEUTREE_SERVER_URL must be set for daily quick start E2E")
	}

	if cfg.APIKey == "" {
		return fmt.Errorf("NEUTREE_API_KEY must be set for daily quick start E2E")
	}

	if cfg.RuntimeRef == "" {
		return fmt.Errorf("E2E_RUNTIME_REF must be set for daily quick start E2E")
	}

	if err := requireDailyReadableFile("E2E_PROFILE_PATH", cfg.ProfilePath); err != nil {
		return err
	}

	if cfg.TestRailRunID != "" || configuredDailyTestRailRunID(profile) != "" {
		return fmt.Errorf("TESTRAIL_RUN_ID and testrail.run_id must be empty for daily quick start E2E")
	}

	switch target {
	case dailyTargetAMD64K8s:
		if err := validateDailyK8sProfile(profile); err != nil {
			return err
		}

		if profile.MockUpstreamHost == "" || profile.MockUpstreamHost == "host.docker.internal" {
			return fmt.Errorf("mock_upstream_host must be explicitly routeable from the remote gateway")
		}
	case dailyTargetARM64K8s:
		return validateDailyK8sProfile(profile)
	case dailyTargetAMD64VM:
		return validateDailyComposeProfile(profile)
	case dailyTargetAMD64L20K8s:
		if err := validateDailyK8sProfile(profile); err != nil {
			return err
		}

		if err := validateDailyEndpointProfile(profile, "sglang"); err != nil {
			return err
		}
	case dailyTargetAMD64T4Static:
		if err := validateDailySSHProfile(profile); err != nil {
			return err
		}

		if err := validateDailyEndpointProfile(profile, "vllm"); err != nil {
			return err
		}

		staticNodeEnabled, err := semver.LessThan(v1.StaticNodeClusterFlowVersionGate, profile.Cluster.Version)
		if err != nil || !staticNodeEnabled {
			return fmt.Errorf("cluster.version must be a valid version greater than %s for static-node daily E2E", v1.StaticNodeClusterFlowVersionGate)
		}
	}

	if isDailyGPUTarget(target) && cfg.ExpectedAcceleratorProduct == "" {
		return fmt.Errorf("%s must be set for daily GPU E2E", envDailyExpectedAcceleratorProduct)
	}

	return nil
}

func isDailyQuickStartTarget(target string) bool {
	switch target {
	case dailyTargetAMD64K8s, dailyTargetARM64K8s, dailyTargetAMD64VM, dailyTargetAMD64L20K8s, dailyTargetAMD64T4Static:
		return true
	default:
		return false
	}
}

func isDailyGPUTarget(target string) bool {
	return target == dailyTargetAMD64L20K8s || target == dailyTargetAMD64T4Static
}

func validateDailyK8sProfile(profile Profile) error {
	if err := requireDailyReadableFile("kubernetes.kubeconfig", profile.Kubernetes.Kubeconfig); err != nil {
		return err
	}

	return validateDailyImageRegistryProfile(profile)
}

func validateDailySSHProfile(profile Profile) error {
	if len(profile.SSHNodes) == 0 || profile.SSHNodes[0].Host == "" {
		return fmt.Errorf("ssh_nodes[0].host must be set for daily SSH E2E")
	}

	return requireDailyReadableFile("ssh_nodes[0].key_file", profile.SSHNodes[0].KeyFile)
}

func validateDailyComposeProfile(profile Profile) error {
	if profile.ControlPlane.DeployMode != "docker-compose" {
		return fmt.Errorf("control_plane.deploy_mode must be docker-compose for the daily VM E2E")
	}

	if profile.Auth.Password == "" {
		return fmt.Errorf("auth.password must be set for the daily VM E2E")
	}

	if profile.ControlPlane.Version == "" {
		return fmt.Errorf("control_plane.version must be set for the daily VM E2E")
	}

	if profile.ControlPlane.ComposeDir == "" {
		return fmt.Errorf("control_plane.compose_dir must be set for the daily VM E2E")
	}

	if profile.ControlPlane.Host == "" || profile.ControlPlane.Host == "local" {
		return nil
	}

	if profile.ControlPlane.SSHUser == "" {
		return fmt.Errorf("control_plane.ssh_user must be set for remote daily VM E2E")
	}

	return requireDailyReadableFile("control_plane.ssh_key", profile.ControlPlane.SSHKey)
}

func validateDailyEndpointProfile(profile Profile, engine string) error {
	if err := validateDailyImageRegistryProfile(profile); err != nil {
		return err
	}

	if profile.ModelRegistry.Type == "" {
		return fmt.Errorf("model_registry.type must be set for daily endpoint E2E")
	}

	if profile.ModelRegistry.URL == "" {
		return fmt.Errorf("model_registry.url must be set for daily endpoint E2E")
	}

	if profile.Model.Name == "" {
		return fmt.Errorf("model.name must be set for daily endpoint E2E")
	}

	if profile.Engines[engine].Version == "" {
		return fmt.Errorf("engines.%s.version must be explicitly set for daily endpoint E2E", engine)
	}

	return nil
}

func validateDailyImageRegistryProfile(profile Profile) error {
	if profile.ImageRegistry.URL == "" {
		return fmt.Errorf("image_registry.url must be set for daily E2E")
	}

	if profile.ImageRegistry.Repository == "" {
		return fmt.Errorf("image_registry.repository must be set for daily E2E")
	}

	return nil
}

func requireDailyReadableFile(name, path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("%s must be set for daily E2E", name)
	}

	file, err := os.Open(expandHome(path))
	if err != nil {
		return fmt.Errorf("%s must reference a readable file: %w", name, err)
	}

	return file.Close()
}

func configuredDailyTestRailRunID(profile Profile) string {
	if profile.Testrail.RunID == nil {
		return ""
	}

	runID := strings.TrimSpace(fmt.Sprintf("%v", profile.Testrail.RunID))
	if runID == "<nil>" {
		return ""
	}

	return runID
}

func selectDailyExpectedAccelerator(cluster v1.Cluster, expectedProduct string) (string, string, error) {
	expectedProduct = strings.TrimSpace(expectedProduct)
	if expectedProduct == "" {
		return "", "", fmt.Errorf("%s must be set for daily GPU E2E", envDailyExpectedAcceleratorProduct)
	}

	if cluster.Status == nil || cluster.Status.ResourceInfo == nil || cluster.Status.ResourceInfo.Allocatable == nil {
		return "", "", fmt.Errorf("cluster does not report allocatable %s resources", v1.AcceleratorTypeNVIDIAGPU)
	}

	group := cluster.Status.ResourceInfo.Allocatable.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU]
	if group == nil {
		return "", "", fmt.Errorf("cluster does not report allocatable %s resources", v1.AcceleratorTypeNVIDIAGPU)
	}

	if _, ok := group.ProductGroups[v1.AcceleratorProduct(expectedProduct)]; !ok {
		return "", "", fmt.Errorf("cluster does not report expected %s product %q", v1.AcceleratorTypeNVIDIAGPU, expectedProduct)
	}

	return string(v1.AcceleratorTypeNVIDIAGPU), expectedProduct, nil
}
