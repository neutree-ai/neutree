package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/Masterminds/semver/v3"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// --- Shared state (set by setup, used by tests) ---

// cliBinary holds the path to the compiled neutree-cli binary.
var cliBinary string

// --- Suite setup / teardown ---

// BuildCLI compiles the neutree-cli binary to a temp directory.
func BuildCLI() {
	tmpDir, err := os.MkdirTemp("", "neutree-e2e-*")
	Expect(err).NotTo(HaveOccurred())

	cliBinary = filepath.Join(tmpDir, "neutree-cli")
	projectRoot := filepath.Join(".", "..", "..")
	buildCmd := exec.Command("go", "build", "-o", cliBinary, "./cmd/neutree-cli/")
	buildCmd.Dir = projectRoot
	buildCmd.Stdout = GinkgoWriter
	buildCmd.Stderr = GinkgoWriter
	Expect(buildCmd.Run()).To(Succeed(), "failed to build neutree-cli")
}

// CleanupCLI removes the compiled binary and its temp directory.
func CleanupCLI() {
	if cliBinary != "" {
		os.RemoveAll(filepath.Dir(cliBinary))
	}
}

// --- CLI execution ---

// CLIResult encapsulates the result of a CLI command execution.
type CLIResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// RunCLI executes the neutree-cli binary with the given arguments.
// It automatically injects --server-url and --api-key from environment variables.
func RunCLI(args ...string) CLIResult {
	return RunCLIWithStdin("", args...)
}

// RunCLIWithStdin executes the neutree-cli binary with stdin input and given arguments.
func RunCLIWithStdin(stdin string, args ...string) CLIResult {
	injected := []string{
		"--server-url", Cfg.ServerURL,
		"--api-key", Cfg.APIKey,
		"--insecure",
	}
	fullArgs := make([]string, 0, len(injected)+len(args))
	fullArgs = append(fullArgs, injected...)
	fullArgs = append(fullArgs, args...)

	cmd := exec.Command(cliBinary, fullArgs...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}

	err := cmd.Run()

	exitCode := 0

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				exitCode = status.ExitStatus()
			} else {
				exitCode = 1
			}
		} else {
			exitCode = 1
		}
	}

	return CLIResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
	}
}

// String returns a human-readable representation of the CLI result.
func (r CLIResult) String() string {
	return fmt.Sprintf("ExitCode: %d\nStdout:\n%s\nStderr:\n%s", r.ExitCode, r.Stdout, r.Stderr)
}

// --- Assertions ---

// ExpectSuccess asserts exit code 0.
func ExpectSuccess(r CLIResult) {
	ExpectWithOffset(1, r.ExitCode).To(Equal(0),
		"expected exit code 0, got %d\nstderr: %s\nstdout: %s", r.ExitCode, r.Stderr, r.Stdout)
}

// ExpectFailed asserts non-zero exit code.
func ExpectFailed(r CLIResult) {
	ExpectWithOffset(1, r.ExitCode).NotTo(Equal(0),
		"expected non-zero exit code\nstderr: %s\nstdout: %s", r.Stderr, r.Stdout)
}

// ExpectStdoutContains asserts stdout contains the given substring.
func ExpectStdoutContains(r CLIResult, substr string) {
	ExpectWithOffset(1, r.Stdout).To(ContainSubstring(substr),
		"stdout does not contain %q\nfull stdout: %s", substr, r.Stdout)
}

// --- SSH remote execution ---

// RunSSH executes a command on a remote host via SSH using the given key file.
func RunSSH(user, host, keyFile, command string) CLIResult {
	args := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ConnectTimeout=10",
		"-o", "BatchMode=yes",
		"-o", "IdentitiesOnly=yes",
		"-i", keyFile,
		fmt.Sprintf("%s@%s", user, host),
		command,
	}

	cmd := exec.Command("ssh", args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				exitCode = status.ExitStatus()
			} else {
				exitCode = 1
			}
		} else {
			exitCode = 1
		}
	}

	return CLIResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
	}
}

// --- Output parsing ---

// ParseTable parses tabwriter table output (header row + data rows) into
// a slice of maps keyed by header names. Column boundaries are detected
// from the header line by looking for gaps of 2+ spaces.
func ParseTable(stdout string) []map[string]string {
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) < 1 {
		return nil
	}

	// Detect column start positions from the header line.
	positions := tableColumnPositions(lines[0])
	headers := extractColumns(lines[0], positions)

	var rows []map[string]string

	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "" {
			continue
		}

		values := extractColumns(line, positions)
		row := make(map[string]string, len(headers))

		for i, h := range headers {
			if i < len(values) {
				row[h] = values[i]
			}
		}

		rows = append(rows, row)
	}

	return rows
}

// tableColumnPositions returns the start index of each column by scanning
// for transitions from a gap of 2+ spaces to a non-space character.
func tableColumnPositions(header string) []int {
	positions := []int{0}

	i := 0
	for i < len(header) {
		if header[i] == ' ' {
			start := i

			for i < len(header) && header[i] == ' ' {
				i++
			}

			if i < len(header) && i-start >= 2 {
				positions = append(positions, i)
			}
		} else {
			i++
		}
	}

	return positions
}

// extractColumns slices a line into column values based on pre-computed positions.
func extractColumns(line string, positions []int) []string {
	cols := make([]string, len(positions))

	for i, start := range positions {
		end := len(line)
		if i+1 < len(positions) {
			end = positions[i+1]
		}

		if start >= len(line) {
			continue
		}

		if end > len(line) {
			end = len(line)
		}

		cols[i] = strings.TrimSpace(line[start:end])
	}

	return cols
}

// ParseKV parses tabwriter key-value output ("Key:  Value" per line) into a map.
func ParseKV(stdout string) map[string]string {
	result := make(map[string]string)

	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if key, value, ok := strings.Cut(line, ":"); ok && key != "" {
			result[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}

	return result
}

// --- Docker helpers ---

// DockerRun runs a container in detached mode and returns its ID.
func DockerRun(args ...string) string {
	fullArgs := append([]string{"run", "-d"}, args...)
	cmd := exec.Command("docker", fullArgs...)
	out, err := cmd.Output()
	ExpectWithOffset(1, err).NotTo(HaveOccurred(),
		"docker run failed: %v", err)

	return strings.TrimSpace(string(out))
}

// DockerRemove force-removes a container, ignoring errors.
func DockerRemove(containerID string) {
	exec.Command("docker", "rm", "-f", containerID).Run() //nolint:errcheck
}

// DockerPort returns the host port mapped to a container port.
func DockerPort(containerID, containerPort string) string {
	cmd := exec.Command("docker", "port", containerID, containerPort)
	out, err := cmd.Output()
	ExpectWithOffset(1, err).NotTo(HaveOccurred(),
		"docker port failed: %v", err)
	// Output format: "0.0.0.0:32768\n" or "[::]:32768\n"
	parts := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)
	_, port, _ := strings.Cut(parts[0], ":")

	return port
}

// StartLocalRegistry starts a registry:2 container on a random port
// and returns (host URL, container ID).
func StartLocalRegistry() (string, string) {
	id := DockerRun("-p", "0:5000", "registry:2")
	port := DockerPort(id, "5000")

	return "localhost:" + port, id
}

// StopLocalRegistry stops and removes the registry container.
func StopLocalRegistry(containerID string) {
	DockerRemove(containerID)
}

// --- Environment helpers ---

// testRegistry returns the model registry name for tests.
func testRegistry() string {
	return "e2e-registry-" + Cfg.RunID
}

// testImageRegistry returns the image registry name for tests.
func testImageRegistry() string {
	return "e2e-image-registry-" + Cfg.RunID
}

// requireSSHEnv returns SSH cluster params from profile. ssh_private_key is returned as base64.
func requireSSHEnv() (headIP, workerIPs, sshUser, sshPrivateKey string) {
	headIP = profileSSHHeadIP()
	if headIP == "" {
		Skip("SSH head IP not configured in profile, skipping SSH cluster tests")
	}

	workerIPs = profileSSHWorkerIPs()

	sshUser = profileSSHUser()
	if sshUser == "" {
		sshUser = "root"
	}

	sshPrivateKey = profileSSHPrivateKey()
	if sshPrivateKey == "" {
		Skip("SSH private key not configured in profile, skipping SSH cluster tests")
	}

	return headIP, workerIPs, sshUser, sshPrivateKey
}

// requireK8sEnv returns the base64-encoded kubeconfig from profile.
func requireK8sEnv() string {
	kubeconfig := profileKubeconfig()
	if kubeconfig == "" {
		Skip("Kubeconfig not configured in profile, skipping K8s cluster tests")
	}

	return kubeconfig
}

// --- Image registry setup/teardown (shared across cluster, endpoint, fault, upgrade tests) ---

var imageRegistryYAML string

// SetupImageRegistry creates an image registry from the YAML template
// and waits for it to reach Connected phase.
func SetupImageRegistry() {
	if imageRegistryYAML != "" {
		return // already set up
	}

	defaults := map[string]string{
		"E2E_IMAGE_REGISTRY":          testImageRegistry(),
		"E2E_WORKSPACE":               profileWorkspace(),
		"E2E_IMAGE_REGISTRY_URL":      profile.ImageRegistry.URL,
		"E2E_IMAGE_REGISTRY_REPO":     profile.ImageRegistry.Repository,
		"E2E_IMAGE_REGISTRY_USERNAME": profile.ImageRegistry.Username,
		"E2E_IMAGE_REGISTRY_PASSWORD": profile.ImageRegistry.Password,
	}
	var err error
	imageRegistryYAML, err = renderTemplateToTempFile(
		filepath.Join("testdata", "image-registry.yaml"), defaults,
	)
	Expect(err).NotTo(HaveOccurred(), "failed to render image registry template")

	r := RunCLI("apply", "-f", imageRegistryYAML)
	ExpectSuccess(r)

	r = RunCLI("wait", "imageregistry", testImageRegistry(),
		"-w", profileWorkspace(),
		"--for", "jsonpath=.status.phase=Connected",
		"--timeout", "2m",
	)
	ExpectSuccess(r)
}

// TeardownImageRegistry deletes the image registry and cleans up the temp YAML.
func TeardownImageRegistry() {
	if imageRegistryYAML != "" {
		RunCLI("delete", "-f", imageRegistryYAML, "--force", "--ignore-not-found")
		os.Remove(imageRegistryYAML)
		imageRegistryYAML = "" // reset so SetupImageRegistry can be called again
	}
}

// waitClusterErrorMessage polls until the cluster's error_message is non-empty or timeout.
func waitClusterErrorMessage(ch *ClusterHelper, name string, timeout time.Duration) v1.Cluster {
	deadline := time.Now().Add(timeout)

	var c v1.Cluster

	for time.Now().Before(deadline) {
		r := ch.Get(name)
		if r.ExitCode == 0 {
			c = parseClusterJSON(r.Stdout)
			if c.Status != nil && c.Status.ErrorMessage != "" {
				return c
			}
		}

		time.Sleep(5 * time.Second)
	}

	return c
}

// requireImageRegistryEnv skips the test if image registry config is missing.
func requireImageRegistryEnv() {
	if profile.ImageRegistry.URL == "" {
		Skip("ImageRegistry.URL not configured in profile")
	}

	if profile.ImageRegistry.Repository == "" {
		Skip("ImageRegistry.Repository not configured in profile")
	}
}

// setupSSHCluster creates an image registry and SSH cluster, waits for Running.
// Returns the cluster name. Caller should defer teardownSSHCluster.
func setupSSHCluster(prefix string) (clusterName string) {
	requireImageRegistryEnv()

	By("Setting up image registry")
	SetupImageRegistry()

	headIP, workerIPs, sshUser, sshPrivateKey := requireSSHEnv()
	clusterName = prefix + Cfg.RunID

	yaml := renderSSHClusterYAML(map[string]string{
		"name":            clusterName,
		"head_ip":         headIP,
		"worker_ips":      workerIPs,
		"ssh_user":        sshUser,
		"ssh_private_key": sshPrivateKey,
	})

	ch := NewClusterHelper()

	By("Applying SSH cluster: " + clusterName)

	r := ch.Apply(yaml)
	ExpectSuccess(r)

	By("Waiting for cluster Running")

	r = ch.WaitForPhase(clusterName, "Running", "10m")
	ExpectSuccess(r)

	return clusterName
}

// setupK8sCluster creates an image registry and K8s cluster, waits for Running.
// Returns the cluster name. Caller should defer teardownCluster.
func setupK8sCluster(prefix string) (clusterName string) {
	requireImageRegistryEnv()

	By("Setting up image registry")
	SetupImageRegistry()

	kubeconfig := requireK8sEnv()
	clusterName = prefix + Cfg.RunID

	yaml := renderK8sClusterYAML(map[string]string{
		"name":       clusterName,
		"kubeconfig": kubeconfig,
	})

	ch := NewClusterHelper()

	By("Applying K8s cluster: " + clusterName)

	r := ch.Apply(yaml)
	ExpectSuccess(r)

	By("Waiting for cluster Running")

	r = ch.WaitForPhase(clusterName, "Running", "10m")
	ExpectSuccess(r)

	return clusterName
}

// teardownCluster deletes a cluster and image registry.
// Uses force delete and ignores errors to ensure cleanup completes even when prior tests failed.
func teardownCluster(clusterName string) {
	ch := NewClusterHelper()

	ch.Delete(clusterName)
	ch.WaitForDelete(clusterName, "10m")

	TeardownImageRegistry()
}

// --- Cluster template rendering ---

// renderSSHClusterYAML renders the SSH cluster template with overrides and returns the temp file path.
// Overrides: name, version, image_registry, head_ip, worker_ips (comma-separated), ssh_user, ssh_private_key.
func renderSSHClusterYAML(overrides map[string]string) string {
	defaults := map[string]string{
		"CLUSTER_NAME":            overrides["name"],
		"CLUSTER_WORKSPACE":       profileWorkspace(),
		"CLUSTER_IMAGE_REGISTRY":  valueOr(overrides, "image_registry", testImageRegistry()),
		"CLUSTER_VERSION":         valueOr(overrides, "version", profileClusterVersion()),
		"CLUSTER_SSH_HEAD_IP":     overrides["head_ip"],
		"CLUSTER_SSH_USER":        overrides["ssh_user"],
		"CLUSTER_SSH_PRIVATE_KEY": overrides["ssh_private_key"],
	}

	// Format accelerator_type YAML block.
	if at := overrides["accelerator_type"]; at != "" {
		defaults["CLUSTER_ACCELERATOR_TYPE_YAML"] = fmt.Sprintf("    accelerator_type: \"%s\"\n", at)
	}

	// Format model_caches YAML block.
	if mc := overrides["model_caches_yaml"]; mc != "" {
		defaults["CLUSTER_MODEL_CACHES_YAML"] = mc
	}

	// Format worker_ips YAML block.
	if workerIPs := overrides["worker_ips"]; workerIPs != "" {
		var buf strings.Builder

		buf.WriteString("        worker_ips:\n")

		for _, ip := range strings.Split(workerIPs, ",") {
			ip = strings.TrimSpace(ip)
			if ip != "" {
				fmt.Fprintf(&buf, "          - \"%s\"\n", ip)
			}
		}

		defaults["CLUSTER_WORKER_IPS_YAML"] = buf.String()
	}

	path, err := renderTemplateToTempFile(filepath.Join("testdata", "ssh-cluster.yaml"), defaults)
	Expect(err).NotTo(HaveOccurred(), "failed to render SSH cluster template")

	return path
}

// renderK8sClusterYAML renders the K8s cluster template with overrides and returns the temp file path.
// Overrides: name, version, image_registry, kubeconfig, router_replicas.
func renderK8sClusterYAML(overrides map[string]string) string {
	defaults := map[string]string{
		"CLUSTER_NAME":            overrides["name"],
		"CLUSTER_WORKSPACE":       profileWorkspace(),
		"CLUSTER_IMAGE_REGISTRY":  valueOr(overrides, "image_registry", testImageRegistry()),
		"CLUSTER_VERSION":         valueOr(overrides, "version", profileClusterVersion()),
		"CLUSTER_KUBECONFIG":      overrides["kubeconfig"],
		"CLUSTER_ROUTER_REPLICAS": valueOr(overrides, "router_replicas", "1"),
		"CLUSTER_ROUTER_CPU":      valueOr(overrides, "router_cpu", "1"),
		"CLUSTER_ROUTER_MEMORY":   valueOr(overrides, "router_memory", "2Gi"),
	}

	if mc := overrides["model_caches_yaml"]; mc != "" {
		defaults["CLUSTER_MODEL_CACHES_YAML"] = mc
	}

	path, err := renderTemplateToTempFile(filepath.Join("testdata", "k8s-cluster.yaml"), defaults)
	Expect(err).NotTo(HaveOccurred(), "failed to render K8s cluster template")

	return path
}

func valueOr(m map[string]string, key, fallback string) string {
	if v, ok := m[key]; ok && v != "" {
		return v
	}

	return fallback
}

// --- ClusterHelper (Page Object for cluster CLI subcommands) ---

// ClusterHelper encapsulates common parameters for cluster CLI operations.
type ClusterHelper struct {
	workspace string
}

// NewClusterHelper creates a ClusterHelper with the test workspace.
func NewClusterHelper() *ClusterHelper {
	return &ClusterHelper{
		workspace: profileWorkspace(),
	}
}

// Apply applies a YAML file with --force-update and removes the temp file afterwards.
func (c *ClusterHelper) Apply(yamlFile string) CLIResult {
	defer os.Remove(yamlFile)
	return RunCLI("apply", "-f", yamlFile, "--force-update")
}

// Get retrieves cluster details as JSON.
func (c *ClusterHelper) Get(name string) CLIResult {
	return RunCLI("get", "cluster", name, "-w", c.workspace, "-o", "json")
}

// Delete deletes a cluster with --force.
func (c *ClusterHelper) Delete(name string) CLIResult {
	return RunCLI("delete", "cluster", name, "-w", c.workspace, "--force")
}

// DeleteGraceful deletes a cluster without --force (graceful shutdown).
func (c *ClusterHelper) DeleteGraceful(name string) CLIResult {
	return RunCLI("delete", "cluster", name, "-w", c.workspace)
}

// WaitForPhase waits for a cluster to reach the specified phase.
func (c *ClusterHelper) WaitForPhase(name, phase, timeout string) CLIResult {
	return RunCLI("wait", "cluster", name,
		"-w", c.workspace,
		"--for", fmt.Sprintf("jsonpath=.status.phase=%s", phase),
		"--timeout", timeout,
	)
}

// WaitForDelete waits for a cluster to be fully deleted.
func (c *ClusterHelper) WaitForDelete(name, timeout string) CLIResult {
	return RunCLI("wait", "cluster", name,
		"-w", c.workspace,
		"--for", "delete",
		"--timeout", timeout,
	)
}

// WaitForSpecChange polls until the observedSpecHash differs from oldHash or
// the phase leaves Running, indicating the controller has started processing the new spec.
// This prevents the race condition where WaitForPhase("Running") returns immediately
// because the cluster is still Running before the controller processes the apply.
func (c *ClusterHelper) WaitForSpecChange(name, oldHash string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		r := c.Get(name)
		if r.ExitCode == 0 {
			cl := parseClusterJSON(r.Stdout)
			if cl.Status.ObservedSpecHash != oldHash {
				return
			}

			if cl.Status.Phase != v1.ClusterPhaseRunning {
				return
			}
		}

		time.Sleep(2 * time.Second)
	}
}

// EnsureDeleted deletes a cluster with --force, ignoring errors (for cleanup).
func (c *ClusterHelper) EnsureDeleted(name string) {
	c.Delete(name)
}

// engineVersionSupportsK8s returns true if the given engine version has K8s deployment templates.
// K8s deployment support starts from v0.11.0 for vLLM.
func engineVersionSupportsK8s(version string) bool {
	minK8sVersion, _ := semver.NewVersion("0.11.0")

	v, err := semver.NewVersion(version)
	if err != nil {
		return false
	}

	return !v.LessThan(minK8sVersion)
}

// --- cluster JSON parsing (uses v1.Cluster) --- `get cluster -o json` ---

func parseClusterJSON(stdout string) v1.Cluster {
	var c v1.Cluster
	ExpectWithOffset(1, json.Unmarshal([]byte(stdout), &c)).To(Succeed())

	return c
}

// clusterAcceleratorType returns the accelerator type from cluster status (e.g., "nvidia_gpu").
func clusterAcceleratorType(c v1.Cluster) string {
	if c.Status != nil && c.Status.AcceleratorType != nil && *c.Status.AcceleratorType != "" {
		return *c.Status.AcceleratorType
	}

	// Fallback: infer from resource_info accelerator_groups keys.
	if c.Status != nil && c.Status.ResourceInfo != nil && c.Status.ResourceInfo.Allocatable != nil {
		for accType := range c.Status.ResourceInfo.Allocatable.AcceleratorGroups {
			return string(accType)
		}
	}

	return ""
}

// clusterAcceleratorProduct returns the first accelerator product from cluster resource_info.
func clusterAcceleratorProduct(c v1.Cluster) string {
	if c.Status == nil || c.Status.ResourceInfo == nil || c.Status.ResourceInfo.Allocatable == nil {
		return ""
	}

	for _, group := range c.Status.ResourceInfo.Allocatable.AcceleratorGroups {
		for product := range group.ProductGroups {
			return string(product)
		}
	}

	return ""
}

// --- Cluster accelerator helpers ---

// getClusterAccelerator returns accelerator type and product from a Running cluster.
func getClusterAccelerator(clusterName string) (accType, accProduct string) {
	c := getClusterFullJSON(clusterName)

	accType = clusterAcceleratorType(c)
	accProduct = clusterAcceleratorProduct(c)

	return accType, accProduct
}

// --- Endpoint helpers (shared by endpoint_ssh_test.go and endpoint_k8s_test.go) ---

// engineArgsYAML returns a YAML snippet for spec.variables.engine_args.
func engineArgsYAML() string {
	raw := profileEngineArgs()
	var lines []string

	for _, pair := range strings.Split(raw, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok || k == "" {
			continue
		}

		lines = append(lines, fmt.Sprintf("      %s: %s", strings.TrimSpace(k), strings.TrimSpace(v)))
	}

	return "\n" + strings.Join(lines, "\n")
}

// applyEndpointOnCluster renders and applies an endpoint on a specific cluster.
func applyEndpointOnCluster(name, cluster, engineVersion string) (yamlPath string) {
	accType, accProduct := getClusterAccelerator(cluster)

	defaults := map[string]string{
		"E2E_ENDPOINT_NAME":       name,
		"E2E_WORKSPACE":           profileWorkspace(),
		"E2E_CLUSTER_NAME":        cluster,
		"E2E_ENGINE_NAME":         profileEngineName(),
		"E2E_ENGINE_VERSION":      engineVersion,
		"E2E_MODEL_REGISTRY":      testRegistry(),
		"E2E_MODEL_NAME":          profileModelName(),
		"E2E_MODEL_VERSION":       profileModelVersion(),
		"E2E_MODEL_TASK":          profileModelTask(),
		"E2E_ACCELERATOR_TYPE":    accType,
		"E2E_ACCELERATOR_PRODUCT": accProduct,
		"E2E_ENGINE_ARGS_YAML":    engineArgsYAML(),
		"E2E_ENV_YAML":            "",
	}

	yamlPath, err := renderTemplateToTempFile(
		filepath.Join("testdata", "endpoint.yaml"), defaults,
	)
	Expect(err).NotTo(HaveOccurred(), "failed to render endpoint template")

	r := RunCLI("apply", "-f", yamlPath, "--force-update")
	ExpectSuccess(r)

	return yamlPath
}

// applyEndpointWithEnv creates an endpoint with custom env vars on the given cluster.
func applyEndpointWithEnv(name, cluster, engineVersion string, env map[string]string) (yamlPath string) {
	accType, accProduct := getClusterAccelerator(cluster)

	var envYAML string
	if len(env) > 0 {
		envYAML = "\n  env:"
		for k, v := range env {
			envYAML += fmt.Sprintf("\n    %s: \"%s\"", k, v)
		}
	}

	defaults := map[string]string{
		"E2E_ENDPOINT_NAME":       name,
		"E2E_WORKSPACE":           profileWorkspace(),
		"E2E_CLUSTER_NAME":        cluster,
		"E2E_ENGINE_NAME":         profileEngineName(),
		"E2E_ENGINE_VERSION":      engineVersion,
		"E2E_MODEL_REGISTRY":      testRegistry(),
		"E2E_MODEL_NAME":          profileModelName(),
		"E2E_MODEL_VERSION":       profileModelVersion(),
		"E2E_MODEL_TASK":          profileModelTask(),
		"E2E_ACCELERATOR_TYPE":    accType,
		"E2E_ACCELERATOR_PRODUCT": accProduct,
		"E2E_ENGINE_ARGS_YAML":    engineArgsYAML(),
		"E2E_ENV_YAML":            envYAML,
	}

	yamlPath, err := renderTemplateToTempFile(
		filepath.Join("testdata", "endpoint.yaml"), defaults,
	)
	Expect(err).NotTo(HaveOccurred(), "failed to render endpoint template")

	r := RunCLI("apply", "-f", yamlPath, "--force-update")
	ExpectSuccess(r)

	return yamlPath
}

// applyFailingEndpoint creates an endpoint with a non-existent model to trigger failure.
func applyFailingEndpoint(name, cluster string) (yamlPath string) {
	accType, accProduct := getClusterAccelerator(cluster)

	defaults := map[string]string{
		"E2E_ENDPOINT_NAME":       name,
		"E2E_WORKSPACE":           profileWorkspace(),
		"E2E_CLUSTER_NAME":        cluster,
		"E2E_ENGINE_NAME":         profileEngineName(),
		"E2E_ENGINE_VERSION":      profileEngineVersion(),
		"E2E_MODEL_REGISTRY":      testRegistry(),
		"E2E_MODEL_NAME":          "non-existent-model-" + Cfg.RunID,
		"E2E_MODEL_VERSION":       "v0.0.0",
		"E2E_MODEL_TASK":          profileModelTask(),
		"E2E_ACCELERATOR_TYPE":    accType,
		"E2E_ACCELERATOR_PRODUCT": accProduct,
		"E2E_ENGINE_ARGS_YAML":    engineArgsYAML(),
		"E2E_ENV_YAML":            "",
	}

	yamlPath, err := renderTemplateToTempFile(filepath.Join("testdata", "endpoint.yaml"), defaults)
	Expect(err).NotTo(HaveOccurred())

	r := RunCLI("apply", "-f", yamlPath)
	ExpectSuccess(r)

	return yamlPath
}

// applyEndpointWithTask renders the endpoint template with custom model/task and applies it.
func applyEndpointWithTask(name, cluster, engineVersion, model, modelVer, task string, extraEngineArgs ...string) (yamlPath string) {
	var buf strings.Builder

	buf.WriteString(engineArgsYAML())

	for _, pair := range extraEngineArgs {
		k, v, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if ok && k != "" {
			fmt.Fprintf(&buf, "\n      %s: %s", strings.TrimSpace(k), strings.TrimSpace(v))
		}
	}

	argsYAML := buf.String()

	accType, accProduct := getClusterAccelerator(cluster)

	defaults := map[string]string{
		"E2E_ENDPOINT_NAME":       name,
		"E2E_WORKSPACE":           profileWorkspace(),
		"E2E_CLUSTER_NAME":        cluster,
		"E2E_ENGINE_NAME":         profileEngineName(),
		"E2E_ENGINE_VERSION":      engineVersion,
		"E2E_MODEL_REGISTRY":      testRegistry(),
		"E2E_MODEL_NAME":          model,
		"E2E_MODEL_VERSION":       modelVer,
		"E2E_MODEL_TASK":          task,
		"E2E_ACCELERATOR_TYPE":    accType,
		"E2E_ACCELERATOR_PRODUCT": accProduct,
		"E2E_ENGINE_ARGS_YAML":    argsYAML,
		"E2E_ENV_YAML":            "",
	}

	yamlPath, err := renderTemplateToTempFile(
		filepath.Join("testdata", "endpoint.yaml"), defaults,
	)
	Expect(err).NotTo(HaveOccurred())

	r := RunCLI("apply", "-f", yamlPath, "--force-update")
	ExpectSuccess(r)

	return yamlPath
}

// allSchemaTypesEngineArgsYAML returns an engine_args YAML snippet covering multiple JSON Schema data types.
// All parameters are chosen from fields accepted by vLLM AsyncEngineArgs to avoid rejection.
//
// Covered types:
//   - string (enum): dtype
//   - integer:        max_model_len
//   - number (float): gpu_memory_utilization
//   - boolean:        enforce_eager
//   - object (JSON):  override_generation_config
func allSchemaTypesEngineArgsYAML() string {
	return `
      dtype: half
      max_model_len: 4096
      gpu_memory_utilization: 0.85
      enforce_eager: true
      override_generation_config: '{}'`
}

// applyEndpointAllSchemaTypes creates an endpoint with engine_args covering all schema data types.
func applyEndpointAllSchemaTypes(name, cluster string) string {
	accType, accProduct := getClusterAccelerator(cluster)

	defaults := map[string]string{
		"E2E_ENDPOINT_NAME":       name,
		"E2E_WORKSPACE":           profileWorkspace(),
		"E2E_CLUSTER_NAME":        cluster,
		"E2E_ENGINE_NAME":         profileEngineName(),
		"E2E_ENGINE_VERSION":      profileEngineVersion(),
		"E2E_MODEL_REGISTRY":      testRegistry(),
		"E2E_MODEL_NAME":          profileModelName(),
		"E2E_MODEL_VERSION":       profileModelVersion(),
		"E2E_MODEL_TASK":          profileModelTask(),
		"E2E_ACCELERATOR_TYPE":    accType,
		"E2E_ACCELERATOR_PRODUCT": accProduct,
		"E2E_ENGINE_ARGS_YAML":    allSchemaTypesEngineArgsYAML(),
	}

	yamlPath, err := renderTemplateToTempFile("testdata/endpoint.yaml", defaults)
	Expect(err).NotTo(HaveOccurred())

	r := RunCLI("apply", "-f", yamlPath, "--force-update")
	ExpectSuccess(r)

	return yamlPath
}

// waitEndpointRunning waits for an endpoint to reach Running phase.
func waitEndpointRunning(name string) {
	r := RunCLI("wait", "endpoint", name,
		"-w", profileWorkspace(),
		"--for", "jsonpath=.status.phase=Running",
		"--timeout", profileEndpointTimeout(),
	)
	ExpectSuccess(r)
}

// waitEndpointFailed waits for an endpoint to reach Failed phase.
func waitEndpointFailed(name string) {
	r := RunCLI("wait", "endpoint", name,
		"-w", profileWorkspace(),
		"--for", "jsonpath=.status.phase=Failed",
		"--timeout", profileEndpointTimeout(),
	)
	ExpectSuccess(r)
}

// getEndpoint retrieves endpoint details as JSON.
func getEndpoint(name string) v1.Endpoint {
	r := RunCLI("get", "endpoint", name, "-w", profileWorkspace(), "-o", "json")
	ExpectSuccess(r)

	return parseEndpointJSON(r.Stdout)
}

// deleteEndpoint deletes an endpoint and waits for it to be removed.
func deleteEndpoint(name string) {
	RunCLI("delete", "endpoint", name, "-w", profileWorkspace(), "--force", "--ignore-not-found")
	RunCLI("wait", "endpoint", name,
		"-w", profileWorkspace(),
		"--for", "delete",
		"--timeout", "5m",
	)
}

// getClusterFullJSON retrieves full cluster JSON for helpers that need ID, Metadata.
func getClusterFullJSON(name string) v1.Cluster {
	r := RunCLI("get", "cluster", name, "-w", profileWorkspace(), "-o", "json")
	ExpectSuccess(r)

	return parseClusterJSON(r.Stdout)
}

func parseEndpointJSON(stdout string) v1.Endpoint {
	var ep v1.Endpoint
	ExpectWithOffset(1, json.Unmarshal([]byte(stdout), &ep)).To(Succeed())

	return ep
}

// --- Inference helpers ---

// doInferenceRequest sends a JSON POST to the given URL path and returns (status_code, body).
func doInferenceRequest(serviceURL, path string, reqBody map[string]any) (int, string) {
	payloadBytes, err := json.Marshal(reqBody)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())

	client := &http.Client{Timeout: 60 * time.Second}

	req, err := http.NewRequest(http.MethodPost,
		strings.TrimRight(serviceURL, "/")+path,
		strings.NewReader(string(payloadBytes)),
	)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	req.Header.Set("Content-Type", "application/json")

	// Cfg.APIKey may already contain "Bearer " prefix (e.g. JWT in upgrade tests)
	authValue := Cfg.APIKey
	if !strings.HasPrefix(authValue, "Bearer ") {
		authValue = "Bearer " + authValue
	}

	req.Header.Set("Authorization", authValue)

	resp, err := client.Do(req)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())

	return resp.StatusCode, string(body)
}

func inferChat(serviceURL, prompt string) (int, string) {
	return doInferenceRequest(serviceURL, "/v1/chat/completions", map[string]any{
		"model": profileModelName(),
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"max_tokens": 16,
	})
}

// inferChatSafe is like inferChat but returns -1 on connection errors instead of failing.
// Use inside Eventually() where transient connection errors are expected.
func inferChatSafe(serviceURL, prompt string) (int, string) {
	payload, _ := json.Marshal(map[string]any{
		"model": profileModelName(),
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"max_tokens": 16,
	})

	client := &http.Client{Timeout: 30 * time.Second}

	req, err := http.NewRequest(http.MethodPost,
		strings.TrimRight(serviceURL, "/")+"/v1/chat/completions",
		strings.NewReader(string(payload)),
	)
	if err != nil {
		return -1, err.Error()
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", bearerAPIKey())

	resp, err := client.Do(req)
	if err != nil {
		return -1, err.Error()
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	return resp.StatusCode, string(body)
}

func inferEmbedding(serviceURL, model, input string) (int, string) {
	return doInferenceRequest(serviceURL, "/v1/embeddings", map[string]any{
		"model": model,
		"input": input,
	})
}

func inferRerank(serviceURL, model, query string, documents []string) (int, string) {
	return doInferenceRequest(serviceURL, "/v1/rerank", map[string]any{
		"model":     model,
		"query":     query,
		"documents": documents,
	})
}

// --- Engine version helpers ---

// requireEngineVersion checks if a specific engine version is available via CLI.
// Skips the test if the version is not found.
func requireEngineVersion(engine, version string) { //nolint:unparam // engine may vary in future
	r := RunCLI("get", "engine", engine, "-o", "json")
	if r.ExitCode != 0 || !strings.Contains(r.Stdout, version) {
		Skip(fmt.Sprintf("Engine %s %s not available, skipping", engine, version))
	}
}

// --- Template rendering ---

// profileVarMap builds a mapping from template variable names to profile values.
// This replaces the old approach of reading env vars for template expansion.
func profileVarMap() map[string]string {
	return map[string]string{
		// SSH
		"E2E_SSH_HEAD_IP":     profileSSHHeadIP(),
		"E2E_SSH_USER":        profileSSHUser(),
		"E2E_SSH_PRIVATE_KEY": profileSSHPrivateKey(),
		"E2E_SSH_WORKER_IPS":  profileSSHWorkerIPs(),

		// Kubernetes
		"E2E_KUBECONFIG": profileKubeconfig(),

		// Image registry
		"E2E_IMAGE_REGISTRY_URL":  profile.ImageRegistry.URL,
		"E2E_IMAGE_REGISTRY_REPO": profile.ImageRegistry.Repository,

		// Model registry
		"E2E_MODEL_REGISTRY_TYPE": profile.ModelRegistry.Type,
		"E2E_MODEL_REGISTRY_URL":  profile.ModelRegistry.URL,

		// Engine
		"E2E_ENGINE_NAME":    profileEngineName(),
		"E2E_ENGINE_VERSION": profileEngineVersion(),

		// Model
		"E2E_MODEL_NAME":    profileModelName(),
		"E2E_MODEL_VERSION": profileModelVersion(),
		"E2E_MODEL_TASK":    profileModelTask(),

		// TestRail (URL/user/password from profile; RUN_ID stays as env var)
		"TESTRAIL_URL":      profile.Testrail.URL,
		"TESTRAIL_USER":     profile.Testrail.User,
		"TESTRAIL_PASSWORD": profile.Testrail.Password,
	}
}

// renderMultiYamlTemplate renders multiple templates and concatenates them into a single
// multi-document YAML temp file separated by "---".
func renderMultiYamlTemplate(templates []struct {
	path     string
	defaults map[string]string
}) (string, error) {
	var parts []string

	for _, t := range templates {
		rendered, err := renderTemplate(t.path, t.defaults)
		if err != nil {
			return "", fmt.Errorf("failed to render %s: %w", t.path, err)
		}

		parts = append(parts, rendered)
	}

	content := strings.Join(parts, "---\n")

	tmpFile, err := os.CreateTemp("", "e2e-multi-*.yaml")
	if err != nil {
		return "", err
	}

	if _, err := tmpFile.WriteString(content); err != nil {
		tmpFile.Close()
		os.Remove(tmpFile.Name())

		return "", err
	}

	tmpFile.Close()

	return tmpFile.Name(), nil
}

// renderTemplate reads a template file and expands ${VAR} references
// using the provided defaults map as primary source, then falling back
// to the profile variable map, then to infrastructure env vars
// (NEUTREE_SERVER_URL, NEUTREE_API_KEY, TESTRAIL_RUN_ID).
func renderTemplate(templatePath string, defaults map[string]string) (string, error) {
	content, err := os.ReadFile(templatePath)
	if err != nil {
		return "", err
	}

	pvm := profileVarMap()

	result := os.Expand(string(content), func(key string) string {
		// 1. Caller-provided defaults take highest priority.
		if defaults != nil {
			if v, ok := defaults[key]; ok && v != "" {
				return v
			}
		}

		// 2. Profile-derived values.
		if v, ok := pvm[key]; ok && v != "" {
			return v
		}

		// 3. For infrastructure env vars (server URL, API key), fall back to os env.
		switch key {
		case "NEUTREE_SERVER_URL", "NEUTREE_API_KEY", "TESTRAIL_RUN_ID":
			return os.Getenv(key)
		}

		return ""
	})

	return result, nil
}

// renderTemplateToTempFile renders a template and writes the result to a temp file.
func renderTemplateToTempFile(templatePath string, defaults map[string]string) (string, error) {
	rendered, err := renderTemplate(templatePath, defaults)
	if err != nil {
		return "", err
	}

	tmpFile, err := os.CreateTemp("", "e2e-*.yaml")
	if err != nil {
		return "", err
	}

	if _, err := tmpFile.WriteString(rendered); err != nil {
		tmpFile.Close()
		os.Remove(tmpFile.Name())

		return "", err
	}

	tmpFile.Close()

	return tmpFile.Name(), nil
}

// bearerAPIKey returns the API key with "Bearer " prefix for Authorization headers.
// Handles the case where Cfg.APIKey already contains the "Bearer " prefix (e.g. JWT).
func bearerAPIKey() string {
	if strings.HasPrefix(Cfg.APIKey, "Bearer ") {
		return Cfg.APIKey
	}

	return "Bearer " + Cfg.APIKey
}

// createAPIKey creates an API key on the server via PostgREST RPC using a JWT token.
// Returns the sk_xxx value.
func createAPIKey(serverURL, jwt, workspace, name string) string {
	reqBody := map[string]any{
		"p_workspace": workspace,
		"p_name":      name,
		"p_quota":     100000,
	}

	bodyBytes, err := json.Marshal(reqBody)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())

	client := &http.Client{Timeout: 30 * time.Second}

	req, err := http.NewRequest(http.MethodPost,
		strings.TrimRight(serverURL, "/")+"/api/v1/rpc/create_api_key",
		strings.NewReader(string(bodyBytes)),
	)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+jwt)

	resp, err := client.Do(req)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	ExpectWithOffset(1, resp.StatusCode).To(Equal(http.StatusOK),
		"create_api_key RPC failed: %s", string(body))

	var result map[string]any
	ExpectWithOffset(1, json.Unmarshal(body, &result)).To(Succeed())

	// sk_value is in status.sk_value
	status, ok := result["status"].(map[string]any)
	ExpectWithOffset(1, ok).To(BeTrue(), "missing status in create_api_key response: %s", string(body))

	skValue, ok := status["sk_value"].(string)
	ExpectWithOffset(1, ok).To(BeTrue(), "missing sk_value in create_api_key response: %s", string(body))
	ExpectWithOffset(1, skValue).NotTo(BeEmpty())

	return skValue
}

// --- Auth helpers (user creation, login, deletion) ---

// createTestUser creates a user via the admin API and returns the user ID.
func createTestUser(username, email, password string) string {
	reqBody := map[string]string{
		"username": username,
		"email":    email,
		"password": password,
	}

	bodyBytes, err := json.Marshal(reqBody)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())

	client := &http.Client{Timeout: 30 * time.Second}

	req, err := http.NewRequest(http.MethodPost,
		strings.TrimRight(Cfg.ServerURL, "/")+"/api/v1/auth/admin/users",
		strings.NewReader(string(bodyBytes)),
	)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", bearerAPIKey())

	resp, err := client.Do(req)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	ExpectWithOffset(1, resp.StatusCode).To(Equal(http.StatusCreated),
		"create user failed: %s", string(body))

	var result map[string]any
	ExpectWithOffset(1, json.Unmarshal(body, &result)).To(Succeed())

	id, ok := result["id"].(string)
	ExpectWithOffset(1, ok).To(BeTrue(), "missing id in create user response: %s", string(body))

	return id
}

// deleteTestUser deletes a user via the GoTrue admin API.
func deleteTestUser(userID string) {
	client := &http.Client{Timeout: 30 * time.Second}

	req, err := http.NewRequest(http.MethodDelete,
		strings.TrimRight(Cfg.ServerURL, "/")+"/api/v1/auth/admin/users/"+userID,
		nil,
	)

	if err != nil {
		return
	}

	req.Header.Set("Authorization", bearerAPIKey())

	resp, err := client.Do(req)
	if err != nil {
		return
	}

	resp.Body.Close()
}

// loginTestUser logs in via GoTrue password grant and returns the access token (JWT).
func loginTestUser(email, password string) string {
	reqBody := map[string]string{
		"email":    email,
		"password": password,
	}

	bodyBytes, err := json.Marshal(reqBody)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())

	client := &http.Client{Timeout: 30 * time.Second}

	req, err := http.NewRequest(http.MethodPost,
		strings.TrimRight(Cfg.ServerURL, "/")+"/api/v1/auth/token?grant_type=password",
		strings.NewReader(string(bodyBytes)),
	)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	ExpectWithOffset(1, resp.StatusCode).To(Equal(http.StatusOK),
		"login failed: %s", string(body))

	var result map[string]any
	ExpectWithOffset(1, json.Unmarshal(body, &result)).To(Succeed())

	token, ok := result["access_token"].(string)
	ExpectWithOffset(1, ok).To(BeTrue(), "missing access_token in login response: %s", string(body))

	return token
}

