package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// --- Shared state (set by setup, used by tests) ---

// cliBinary holds the path to the compiled neutree-cli binary.
var cliBinary string

// Cluster phase timeouts shared across E2E tests.
//
//   - IntermediatePhaseTimeout applies to transient phases (Initializing,
//     Updating, Upgrading, Deleting) on the happy path, where the controller
//     is expected to act within seconds.
//   - TerminalPhaseTimeout applies to stable phases (Running, Deleted) and
//     to fault / anomaly scenarios where the controller must surface an
//     underlying failure (SSH / TCP timeout, unreachable image registry,
//     Ray health-check before Failed), both of which can take minutes.
const (
	IntermediatePhaseTimeout = 30 * time.Second
	TerminalPhaseTimeout     = 10 * time.Minute
)

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

// requireImageRegistryEnv skips the test if image registry config is missing.
func requireImageRegistryEnv() {
	if profile.ImageRegistry.URL == "" {
		Skip("ImageRegistry.URL not configured in profile")
	}

	if profile.ImageRegistry.Repository == "" {
		Skip("ImageRegistry.Repository not configured in profile")
	}
}

// --- Image registry setup/teardown ---

var imageRegistryYAML string

// SetupImageRegistry creates an image registry and waits for Connected phase.
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
		imageRegistryYAML = ""
	}
}

// --- Cluster template rendering ---

// renderSSHClusterYAML renders the SSH cluster template with overrides and returns the temp file path.
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

	if at := overrides["accelerator_type"]; at != "" {
		defaults["CLUSTER_ACCELERATOR_TYPE_YAML"] = fmt.Sprintf("    accelerator_type: \"%s\"\n", at)
	}

	if mc := overrides["model_caches_yaml"]; mc != "" {
		defaults["CLUSTER_MODEL_CACHES_YAML"] = mc
	}

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

// --- ClusterHelper ---

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

// Delete deletes a cluster with --force --ignore-not-found.
func (c *ClusterHelper) Delete(name string) CLIResult {
	return RunCLI("delete", "cluster", name, "-w", c.workspace, "--force", "--ignore-not-found")
}

// DeleteGraceful deletes a cluster without --force (graceful shutdown).
// Blocks until the resource is fully gone (CLI default --wait=true).
func (c *ClusterHelper) DeleteGraceful(name string) CLIResult {
	return RunCLI("delete", "cluster", name, "-w", c.workspace)
}

// DeleteAsync issues a graceful delete but returns as soon as the request is
// accepted (--wait=false), so callers can still observe the intermediate
// Deleting phase via EventuallyInPhase. Use DeleteGraceful when you don't
// need to observe the transient phase.
func (c *ClusterHelper) DeleteAsync(name string) CLIResult {
	return RunCLI("delete", "cluster", name, "-w", c.workspace, "--wait=false")
}

// WaitForPhase waits for a cluster to reach the specified phase.
func (c *ClusterHelper) WaitForPhase(name string, phase v1.ClusterPhase, timeout time.Duration) CLIResult {
	return RunCLI("wait", "cluster", name,
		"-w", c.workspace,
		"--for", fmt.Sprintf("jsonpath=.status.phase=%s", phase),
		"--timeout", timeout.String(),
	)
}

// WaitForDelete waits for a cluster to be fully deleted.
func (c *ClusterHelper) WaitForDelete(name string, timeout time.Duration) CLIResult {
	return RunCLI("wait", "cluster", name,
		"-w", c.workspace,
		"--for", "delete",
		"--timeout", timeout.String(),
	)
}

// checkClusterStatus compares the observed cluster status against phase and
// an optional error_message substring. errMatch == "" skips the error_message
// check (some phases like Upgrading use error_message as a progress log, not
// a real error). When callers need "error_message must be empty", they should
// chain an explicit Expect(BeEmpty()) on the returned cluster.
// Returns nil when the cluster matches.
func checkClusterStatus(cl v1.Cluster, phase v1.ClusterPhase, errMatch string) error {
	if cl.Status == nil {
		return fmt.Errorf("status is nil")
	}

	if cl.Status.Phase != phase {
		return fmt.Errorf("phase=%q, want %q", cl.Status.Phase, phase)
	}

	if errMatch != "" && !strings.Contains(cl.Status.ErrorMessage, errMatch) {
		return fmt.Errorf("error_message=%q, want contains %q", cl.Status.ErrorMessage, errMatch)
	}

	return nil
}

// observeCluster fetches and parses the cluster, returning (cluster, error).
// error is non-nil when the CLI call fails so the caller can skip this tick.
func (c *ClusterHelper) observeCluster(name string) (v1.Cluster, error) {
	r := c.Get(name)
	if r.ExitCode != 0 {
		return v1.Cluster{}, fmt.Errorf("get cluster %q exit %d", name, r.ExitCode)
	}

	return parseClusterJSON(r.Stdout), nil
}

// EventuallyInPhase asserts that within timeout the cluster reaches phase.
// errMatch == "" skips the error_message check; a non-empty errMatch requires
// error_message to contain the substring. Polls at 500ms. Returns the last
// observed cluster.
func (c *ClusterHelper) EventuallyInPhase(name string, phase v1.ClusterPhase, errMatch string, timeout time.Duration) v1.Cluster {
	var last v1.Cluster

	EventuallyWithOffset(1, func() error {
		cl, err := c.observeCluster(name)
		if err != nil {
			return err
		}

		last = cl

		return checkClusterStatus(cl, phase, errMatch)
	}, timeout, 500*time.Millisecond).Should(Succeed(),
		"cluster %q should reach phase %q (errMatch=%q) within %s", name, phase, errMatch, timeout)

	return last
}

// observeClusterTransition polls at 500ms and reports whether both the target
// phase and an extra signal (e.g., hash or version change) were observed
// within timeout. Signals are tracked cumulatively so a sub-500ms phase
// window is still captured as long as a later tick shows the extra signal.
// Does not assert; callers decide how to report failures.
func (c *ClusterHelper) observeClusterTransition(name string, phase v1.ClusterPhase, extra func(v1.Cluster) bool, timeout time.Duration) (seenPhase, seenExtra bool) {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		r := c.Get(name)
		if r.ExitCode == 0 {
			cl := parseClusterJSON(r.Stdout)
			if cl.Status.Phase == phase {
				seenPhase = true
			}

			if extra(cl) {
				seenExtra = true
			}

			if seenPhase && seenExtra {
				return
			}
		}

		time.Sleep(500 * time.Millisecond)
	}

	return
}

// WaitForClusterUpdating asserts the cluster enters Updating phase and
// observedSpecHash changes from oldHash within timeout.
func (c *ClusterHelper) WaitForClusterUpdating(name, oldHash string, timeout time.Duration) {
	seenPhase, seenExtra := c.observeClusterTransition(name, v1.ClusterPhaseUpdating,
		func(cl v1.Cluster) bool { return cl.Status.ObservedSpecHash != oldHash }, timeout)

	ExpectWithOffset(1, seenPhase).To(BeTrue(),
		"cluster %q did not enter Updating phase within %s", name, timeout)
	ExpectWithOffset(1, seenExtra).To(BeTrue(),
		"cluster %q observedSpecHash did not change within %s", name, timeout)
}

// WaitForSpecChange polls until the observedSpecHash differs from oldHash or
// the phase leaves Running, preventing the race where WaitForPhase("Running")
// returns immediately before the controller processes a new apply.
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

// EnsureDeleted deletes a cluster and waits for full removal (for cleanup).
func (c *ClusterHelper) EnsureDeleted(name string) {
	c.Delete(name)
	c.WaitForDelete(name, TerminalPhaseTimeout)
}

// --- Cluster JSON parsing ---

func parseClusterJSON(stdout string) v1.Cluster {
	var c v1.Cluster
	ExpectWithOffset(1, json.Unmarshal([]byte(stdout), &c)).To(Succeed())

	return c
}

func getClusterFullJSON(name string) v1.Cluster {
	r := RunCLI("get", "cluster", name, "-w", profileWorkspace(), "-o", "json")
	ExpectSuccess(r)

	return parseClusterJSON(r.Stdout)
}

// --- Endpoint helpers (new, used by cluster upgrade tests) ---

// applyEndpointWithEnv creates an endpoint with custom env vars on the given cluster.
func applyEndpointWithEnv(name, cluster, engineVersion string, env map[string]string) (yamlPath string) {
	var envYAML string
	if len(env) > 0 {
		envYAML = "\n  env:"
		for k, v := range env {
			envYAML += fmt.Sprintf("\n    %s: \"%s\"", k, v)
		}
	}

	// Inline engine args YAML generation (same logic as engineArgsYAML in endpoint_test.go).
	raw := profileEngineArgs()

	var argsLines []string

	for _, pair := range strings.Split(raw, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok || k == "" {
			continue
		}

		argsLines = append(argsLines, fmt.Sprintf("      %s: %s", strings.TrimSpace(k), strings.TrimSpace(v)))
	}

	argsYAML := "\n" + strings.Join(argsLines, "\n")

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
		"E2E_ACCELERATOR_TYPE":    profileAcceleratorType(),
		"E2E_ACCELERATOR_PRODUCT": profileAcceleratorProduct(),
		"E2E_ENGINE_ARGS_YAML":    argsYAML,
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

// --- Template rendering ---

// profileVarMap builds a mapping from template variable names to profile values.
// This replaces the old approach of reading env vars for template expansion.
func profileVarMap() map[string]string {
	return map[string]string{
		// Workspace
		"E2E_WORKSPACE": profileWorkspace(),

		// SSH
		"E2E_SSH_HEAD_IP":     profileSSHHeadIP(),
		"E2E_SSH_USER":        profileSSHUser(),
		"E2E_SSH_PRIVATE_KEY": profileSSHPrivateKey(),
		"E2E_SSH_WORKER_IPS":  profileSSHWorkerIPs(),

		// Kubernetes
		"E2E_KUBECONFIG": profileKubeconfig(),

		// Image registry
		"E2E_IMAGE_REGISTRY_URL":      profile.ImageRegistry.URL,
		"E2E_IMAGE_REGISTRY_REPO":     profile.ImageRegistry.Repository,
		"E2E_IMAGE_REGISTRY_USERNAME": profile.ImageRegistry.Username,
		"E2E_IMAGE_REGISTRY_PASSWORD": profile.ImageRegistry.Password,

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
		// 1. Caller-provided defaults take highest priority (including empty string).
		if defaults != nil {
			if v, ok := defaults[key]; ok {
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

// requireImageRegistryProfile skips the test if image registry is not fully configured in profile.
func requireImageRegistryProfile() {
	if profile.ImageRegistry.URL == "" {
		Skip("ImageRegistry.URL not configured in profile")
	}

	if profile.ImageRegistry.Repository == "" {
		Skip("ImageRegistry.Repository not configured in profile")
	}
}

// renderImageRegistryYAML renders an ImageRegistry YAML and returns the temp file path.
func renderImageRegistryYAML(overrides map[string]string) string {
	path, err := renderTemplateToTempFile("testdata/image-registry.yaml", map[string]string{
		"E2E_IMAGE_REGISTRY": overrides["name"],
	})
	ExpectWithOffset(1, err).NotTo(HaveOccurred())

	return path
}

// writeMultiDocYAML reads multiple rendered YAML temp files, concatenates them into
// a single multi-document YAML file separated by "---", and returns the combined file path.
// Callers are responsible for cleaning up the input files.
func writeMultiDocYAML(paths ...string) string {
	var parts []string

	for _, p := range paths {
		content, err := os.ReadFile(p)
		ExpectWithOffset(1, err).NotTo(HaveOccurred(), "failed to read %s", p)

		parts = append(parts, string(content))
	}

	combined := strings.Join(parts, "---\n")

	tmpFile, err := os.CreateTemp("", "e2e-multi-*.yaml")
	ExpectWithOffset(1, err).NotTo(HaveOccurred())

	_, err = tmpFile.WriteString(combined)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	tmpFile.Close()

	return tmpFile.Name()
}
