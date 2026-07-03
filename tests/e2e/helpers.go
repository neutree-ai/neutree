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
	"text/template"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/Masterminds/semver/v3"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// ModelCache describes one model cache entry for cluster spec.config.model_caches.
// Mode selects which sub-block the template renders.
type ModelCache struct {
	Name string
	Mode string // "host_path" | "nfs" | "pvc"

	// host_path mode
	HostPath string

	// nfs mode
	NFSServer string
	NFSPath   string

	// pvc mode
	PVCStorageClass string
	PVCStorage      string
}

// EngineArg is a single key/value pair for endpoint spec.variables.engine_args.
type EngineArg struct {
	Key   string
	Value string
}

// --- Shared state (set by setup, used by tests) ---

// cliBinary holds the path to the compiled neutree-cli binary.
var cliBinary string
var cleanupCLIBinary bool

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
	resolved, cleanup, err := resolveCLIBinary()
	Expect(err).NotTo(HaveOccurred())

	cliBinary = resolved
	cleanupCLIBinary = cleanup

	if !cleanup {
		return
	}
}

func resolveCLIBinary() (string, bool, error) {
	if cliPath := os.Getenv("E2E_CLI_BINARY"); cliPath != "" {
		if _, err := os.Stat(cliPath); err != nil {
			return "", false, err
		}

		return cliPath, false, nil
	}

	tmpDir, err := os.MkdirTemp("", "neutree-e2e-*")
	if err != nil {
		return "", false, err
	}

	binary := filepath.Join(tmpDir, "neutree-cli")
	projectRoot := filepath.Join(".", "..", "..")
	buildCmd := exec.Command("go", "build", "-o", binary, "./cmd/neutree-cli/")
	buildCmd.Dir = projectRoot
	buildCmd.Stdout = GinkgoWriter
	buildCmd.Stderr = GinkgoWriter

	if err := buildCmd.Run(); err != nil {
		return "", false, err
	}

	return binary, true, nil
}

// CleanupCLI removes the compiled binary and its temp directory.
func CleanupCLI() {
	if cliBinary != "" && cleanupCLIBinary {
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

// requireSSHProfile returns SSH cluster params from profile. ssh_private_key is returned as base64.
// workerIPs is returned as a slice (already split from comma-separated form).
func requireSSHProfile() (headIP string, workerIPs []string, sshUser, sshPrivateKey string) {
	headIP = profileSSHHeadIP()
	if headIP == "" {
		Skip("SSH head IP not configured in profile, skipping SSH cluster tests")
	}

	if raw := profileSSHWorkerIPs(); raw != "" {
		for _, ip := range strings.Split(raw, ",") {
			ip = strings.TrimSpace(ip)
			if ip != "" {
				workerIPs = append(workerIPs, ip)
			}
		}
	}

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

// requireK8sProfile returns the base64-encoded kubeconfig from profile.
func requireK8sProfile() string {
	kubeconfig := profileKubeconfig()
	if kubeconfig == "" {
		Skip("Kubeconfig not configured in profile, skipping K8s cluster tests")
	}

	return kubeconfig
}

// --- Image registry setup/teardown ---

var imageRegistryYAML string

// SetupImageRegistry creates an image registry and waits for Connected phase.
func SetupImageRegistry() {
	if imageRegistryYAML != "" {
		return // already set up
	}

	defaults := map[string]any{
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
		r := RunCLI("delete", "-f", imageRegistryYAML, "--force", "--ignore-not-found")
		ExpectSuccess(r)
		os.Remove(imageRegistryYAML)
		imageRegistryYAML = ""
	}
}

// --- Cluster template rendering ---

// renderSSHClusterYAML renders the SSH cluster template with overrides and returns the temp file path.
//
// Recognized overrides keys:
//   - "name"            (string)         cluster name
//   - "image_registry"  (string)         defaults to testImageRegistry()
//   - "version"         (string)         defaults to profileClusterVersion()
//   - "head_ip"         (string)
//   - "ssh_user"        (string)
//   - "ssh_private_key" (string)
//   - "worker_ips"      ([]string)       worker IP list (template-rendered as range)
//   - "accelerator_type"(string)         optional
//   - "accelerator_exporter_mode" (string) optional
//   - "model_caches"    ([]ModelCache)   optional, template-rendered per Mode
func renderSSHClusterYAML(overrides map[string]any) string {
	data := map[string]any{
		"CLUSTER_NAME":                      stringOr(overrides, "name", ""),
		"CLUSTER_WORKSPACE":                 profileWorkspace(),
		"CLUSTER_IMAGE_REGISTRY":            stringOr(overrides, "image_registry", testImageRegistry()),
		"CLUSTER_VERSION":                   stringOr(overrides, "version", profileClusterVersion()),
		"CLUSTER_SSH_HEAD_IP":               stringOr(overrides, "head_ip", ""),
		"CLUSTER_SSH_USER":                  stringOr(overrides, "ssh_user", ""),
		"CLUSTER_SSH_PRIVATE_KEY":           stringOr(overrides, "ssh_private_key", ""),
		"CLUSTER_ACCELERATOR_TYPE":          stringOr(overrides, "accelerator_type", ""),
		"CLUSTER_ACCELERATOR_EXPORTER_MODE": stringOr(overrides, "accelerator_exporter_mode", ""),
		"CLUSTER_MODEL_CACHES":              anySliceOr[ModelCache](overrides, "model_caches", nil),
		"CLUSTER_WORKER_IPS":                anySliceOr[string](overrides, "worker_ips", nil),
	}

	path, err := renderTemplateToTempFile(filepath.Join("testdata", "ssh-cluster.yaml"), data)
	Expect(err).NotTo(HaveOccurred(), "failed to render SSH cluster template")

	return path
}

// renderK8sClusterYAML renders the K8s cluster template with overrides and returns the temp file path.
//
// Recognized overrides keys:
//   - "name"             (string)
//   - "image_registry"   (string)         defaults to testImageRegistry()
//   - "version"          (string)         defaults to profileClusterVersion()
//   - "kubeconfig"       (string)
//   - "router_replicas"  (string)         defaults to "1"
//   - "router_cpu"       (string)         defaults to "1"
//   - "router_memory"    (string)         defaults to "2Gi"
//   - "model_caches"     ([]ModelCache)   optional
//   - "accelerator_exporter_mode" (string) optional
//   - "accelerator_virtualization_enabled" (bool) optional
func renderK8sClusterYAML(overrides map[string]any) string {
	data := map[string]any{
		"CLUSTER_NAME":                               stringOr(overrides, "name", ""),
		"CLUSTER_WORKSPACE":                          profileWorkspace(),
		"CLUSTER_IMAGE_REGISTRY":                     stringOr(overrides, "image_registry", testImageRegistry()),
		"CLUSTER_VERSION":                            stringOr(overrides, "version", profileClusterVersion()),
		"CLUSTER_KUBECONFIG":                         stringOr(overrides, "kubeconfig", ""),
		"CLUSTER_ROUTER_REPLICAS":                    stringOr(overrides, "router_replicas", "1"),
		"CLUSTER_ROUTER_CPU":                         stringOr(overrides, "router_cpu", "1"),
		"CLUSTER_ROUTER_MEMORY":                      stringOr(overrides, "router_memory", "2Gi"),
		"CLUSTER_MODEL_CACHES":                       anySliceOr[ModelCache](overrides, "model_caches", nil),
		"CLUSTER_ACCELERATOR_EXPORTER_MODE":          stringOr(overrides, "accelerator_exporter_mode", ""),
		"CLUSTER_ACCELERATOR_VIRTUALIZATION_ENABLED": boolOr(overrides, "accelerator_virtualization_enabled", false),
	}

	path, err := renderTemplateToTempFile(filepath.Join("testdata", "k8s-cluster.yaml"), data)
	Expect(err).NotTo(HaveOccurred(), "failed to render K8s cluster template")

	return path
}

// stringOr returns m[key] as string, falling back to fallback if missing or empty.
func stringOr(m map[string]any, key, fallback string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}

	return fallback
}

func boolOr(m map[string]any, key string, fallback bool) bool {
	if v, ok := m[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}

	return fallback
}

// anySliceOr returns m[key] coerced to []T. If the value is missing or nil,
// fallback is returned. If the value is present but NOT assignable to []T,
// the test fails loudly — silent fallback would hide caller-side type errors
// (e.g. passing a comma-separated string for a key that expects []string).
func anySliceOr[T any](m map[string]any, key string, fallback []T) []T {
	v, ok := m[key]
	if !ok || v == nil {
		return fallback
	}

	s, ok := v.([]T)
	if !ok {
		Fail(fmt.Sprintf("anySliceOr: key %q expects []%T, got %T (value: %v)",
			key, *new(T), v, v))
	}

	return s
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
//
// Special case for the Deleted phase: once the resource is GC'd it cannot be
// retrieved, but the SSH cluster delete flow only stays in Deleted for a few
// hundred milliseconds before GC — faster than the 500ms poll interval. A
// "not found" read is treated as a synthetic Deleted sighting so callers
// watching for Deleted don't miss the transient phase. Any caller watching
// for a non-Deleted phase will see a phase mismatch and keep polling as
// normal.
func (c *ClusterHelper) observeCluster(name string) (v1.Cluster, error) {
	r := c.Get(name)
	if r.ExitCode != 0 {
		if strings.Contains(r.Stdout, "not found") || strings.Contains(r.Stderr, "not found") {
			return v1.Cluster{Status: &v1.ClusterStatus{Phase: v1.ClusterPhaseDeleted}}, nil
		}

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

// EventuallyObservedSpecHashAdvanced polls the cluster until Status.ObservedSpecHash
// differs from oldHash or the timeout fires. Use after Apply to confirm the
// controller has observed the new spec. The hash is only written when phase
// reaches Running, so any reconcile error path will keep the hash pinned.
func (c *ClusterHelper) EventuallyObservedSpecHashAdvanced(name, oldHash string, timeout time.Duration) {
	EventuallyWithOffset(1, func() string {
		r := c.Get(name)
		if r.ExitCode != 0 {
			return oldHash
		}

		return parseClusterJSON(r.Stdout).Status.ObservedSpecHash
	}, timeout, 500*time.Millisecond).ShouldNot(Equal(oldHash),
		"cluster %q Status.ObservedSpecHash should advance from %q within %s", name, oldHash, timeout)
}

// EnsureDeleted deletes a cluster and waits for full removal (for cleanup).
func (c *ClusterHelper) EnsureDeleted(name string) {
	r := c.Delete(name)
	ExpectSuccess(r)

	r = c.WaitForDelete(name, TerminalPhaseTimeout)
	ExpectSuccess(r)
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

// renderTemplate reads a template file and renders it via text/template, using
// the merged data map (caller > profile > env). Strict missing-key errors so
// callers must provide every variable referenced in the template.
func renderTemplate(templatePath string, data map[string]any) (string, error) {
	content, err := os.ReadFile(templatePath)
	if err != nil {
		return "", err
	}

	tmpl, err := template.New(filepath.Base(templatePath)).
		Option("missingkey=error").
		Parse(string(content))
	if err != nil {
		return "", fmt.Errorf("parse template %s: %w", templatePath, err)
	}

	merged := mergeData(data, profileVarMap(), infraEnvVars())

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, merged); err != nil {
		return "", fmt.Errorf("execute template %s: %w", templatePath, err)
	}

	return buf.String(), nil
}

// mergeData merges data with priority: caller > profile > env. Empty profile/env
// values are skipped (so defaults can fall through), but caller values are kept
// even when empty so callers can explicitly override profile to "".
func mergeData(callerData map[string]any, profileVars, envVars map[string]string) map[string]any {
	out := map[string]any{}

	for k, v := range envVars {
		if v != "" {
			out[k] = v
		}
	}

	for k, v := range profileVars {
		if v != "" {
			out[k] = v
		}
	}

	for k, v := range callerData {
		out[k] = v
	}

	return out
}

// infraEnvVars returns infrastructure-level env vars that templates may reference.
func infraEnvVars() map[string]string {
	return map[string]string{
		"NEUTREE_SERVER_URL": os.Getenv("NEUTREE_SERVER_URL"),
		"NEUTREE_API_KEY":    os.Getenv("NEUTREE_API_KEY"),
		"TESTRAIL_RUN_ID":    os.Getenv("TESTRAIL_RUN_ID"),
	}
}

// renderTemplateToTempFile renders a template and writes the result to a temp file.
func renderTemplateToTempFile(templatePath string, data map[string]any) (string, error) {
	rendered, err := renderTemplate(templatePath, data)
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
//
// Recognized overrides keys:
//   - "name" (string)
func renderImageRegistryYAML(overrides map[string]any) string {
	path, err := renderTemplateToTempFile("testdata/image-registry.yaml", map[string]any{
		"E2E_IMAGE_REGISTRY": stringOr(overrides, "name", ""),
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

// --- Auth helpers ---

// createTestUser creates a user via the admin API and returns the user ID.
// token must be an admin JWT (not an API key).
func createTestUser(token, username, email, password string) string {
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
		bytes.NewReader(bodyBytes),
	)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	ExpectWithOffset(1, resp.StatusCode).To(BeElementOf(http.StatusOK, http.StatusCreated),
		"create user failed: %s", string(body))

	var result map[string]any
	ExpectWithOffset(1, json.Unmarshal(body, &result)).To(Succeed())

	id, ok := result["id"].(string)
	ExpectWithOffset(1, ok).To(BeTrue(), "missing id in create user response: %s", string(body))

	return id
}

// deleteTestUser deletes a user via the admin API (best-effort, ignores errors).
// token must be an admin JWT.
func deleteTestUser(token, userID string) {
	client := &http.Client{Timeout: 30 * time.Second}

	req, err := http.NewRequest(http.MethodDelete,
		strings.TrimRight(Cfg.ServerURL, "/")+"/api/v1/auth/admin/users/"+userID,
		nil,
	)
	if err != nil {
		return
	}

	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return
	}

	resp.Body.Close()
}

// --- Control Plane helpers (auth, API key, inference) ---

// loginTestUser logs in via GoTrue password grant and returns the access token (JWT).
// If serverURL is empty, Cfg.ServerURL is used.
func loginTestUser(serverURL, email, password string) (string, error) {
	if serverURL == "" {
		serverURL = Cfg.ServerURL
	}

	bodyBytes, err := json.Marshal(map[string]string{
		"email":    email,
		"password": password,
	})
	if err != nil {
		return "", err
	}

	client := &http.Client{Timeout: 30 * time.Second}

	resp, err := client.Post(
		strings.TrimRight(serverURL, "/")+"/api/v1/auth/token?grant_type=password",
		"application/json",
		strings.NewReader(string(bodyBytes)),
	)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("login failed (HTTP %d): %s", resp.StatusCode, string(body))
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("invalid login response: %w", err)
	}

	token, ok := result["access_token"].(string)
	if !ok || token == "" {
		return "", fmt.Errorf("missing access_token in login response: %s", string(body))
	}

	return token, nil
}

// createAPIKey creates an API key on the server via PostgREST RPC using a JWT token.
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

	var apiKey v1.ApiKey
	ExpectWithOffset(1, json.Unmarshal(body, &apiKey)).To(Succeed(),
		"failed to parse create_api_key response: %s", string(body))
	ExpectWithOffset(1, apiKey.Status).NotTo(BeNil(),
		"create_api_key response missing status: %s", string(body))
	ExpectWithOffset(1, apiKey.Status.SkValue).NotTo(BeEmpty(),
		"create_api_key response missing sk_value: %s", string(body))

	return apiKey.Status.SkValue
}

// callNeutreeAPI performs an HTTP request against neutree-api regular
// endpoints (those served at `Cfg.ServerURL/api/v1/...` other than
// /api/v1/auth/* and /api/v1/rpc/*).
//
// Three auth conventions exist in this file and they are not interchangeable:
//   - createTestUser / deleteTestUser / createAPIKey: Bearer <jwt>, against
//     /api/v1/auth/admin/* and /api/v1/rpc/* — admin user JWT middleware.
//   - doInferenceRequest: Bearer <api_key> against the Kong-routed inference
//     gateway (serviceURL, NOT Cfg.ServerURL).
//   - This helper: raw <api_key> (no Bearer prefix) against the regular
//     /api/v1/... routes. Sending a Bearer-prefixed key here returns
//     401 invalid_token because the middleware compares against the
//     stored sk_value verbatim.
//
// TODO: unify all of the above (admin JWT, Kong inference, raw API key)
// into a single api_helper layer. Today the three call patterns share
// boilerplate (NewRequest + Client{Timeout} + Do + ReadAll) but are
// duplicated across 6+ functions; a shared low-level httpJSON plus three
// thin auth wrappers (Admin/Inference/API) would deduplicate the
// boilerplate without merging the auth conventions, and would give
// future tests one obvious place to look for "how do I call the API".
func callNeutreeAPI(method, path string) ([]byte, int) {
	return callNeutreeAPIWithBody(method, path, nil)
}

func callNeutreeAPIWithJSON(method, path string, payload any) ([]byte, int) {
	GinkgoHelper()

	body, err := json.Marshal(payload)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())

	return callNeutreeAPIWithBody(method, path, bytes.NewReader(body))
}

func callNeutreeAPIWithBody(method, path string, requestBody io.Reader) ([]byte, int) {
	GinkgoHelper()

	url := strings.TrimRight(Cfg.ServerURL, "/") + path
	req, err := http.NewRequest(method, url, requestBody)
	Expect(err).NotTo(HaveOccurred())

	req.Header.Set("Authorization", Cfg.APIKey)

	if requestBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	Expect(err).NotTo(HaveOccurred())

	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	Expect(err).NotTo(HaveOccurred())

	return body, resp.StatusCode
}

// --- Endpoint/inference/accelerator helpers ---

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

// clusterAcceleratorType returns the accelerator type from cluster status (e.g., "nvidia_gpu").
func clusterAcceleratorType(c v1.Cluster) string {
	if c.Status != nil && c.Status.AcceleratorType != nil && *c.Status.AcceleratorType != "" {
		return *c.Status.AcceleratorType
	}

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

// getClusterAccelerator returns accelerator type and product from a Running cluster.
func getClusterAccelerator(clusterName string) (accType, accProduct string) {
	c := getClusterFullJSON(clusterName)

	accType = clusterAcceleratorType(c)
	accProduct = clusterAcceleratorProduct(c)

	return accType, accProduct
}

// setupSSHCluster creates an image registry and SSH cluster, waits for Running.
// Returns the cluster name. Caller should defer teardownCluster.
func setupSSHCluster(prefix string) (clusterName string) {
	requireImageRegistryProfile()

	By("Setting up image registry")
	SetupImageRegistry()

	headIP, workerIPs, sshUser, sshPrivateKey := requireSSHProfile()
	clusterName = prefix + Cfg.RunID

	yaml := renderSSHClusterYAML(map[string]any{
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
	ch.EventuallyInPhase(clusterName, v1.ClusterPhaseRunning, "", TerminalPhaseTimeout)

	return clusterName
}

// setupK8sCluster creates an image registry and K8s cluster, waits for Running.
// Returns the cluster name. Caller should defer teardownCluster.
func setupK8sCluster(prefix string) (clusterName string) {
	requireImageRegistryProfile()

	By("Setting up image registry")
	SetupImageRegistry()

	kubeconfig := requireK8sProfile()
	clusterName = prefix + Cfg.RunID

	yaml := renderK8sClusterYAML(map[string]any{
		"name":       clusterName,
		"kubeconfig": kubeconfig,
	})

	ch := NewClusterHelper()

	By("Applying K8s cluster: " + clusterName)

	r := ch.Apply(yaml)
	ExpectSuccess(r)

	By("Waiting for cluster Running")
	ch.EventuallyInPhase(clusterName, v1.ClusterPhaseRunning, "", TerminalPhaseTimeout)

	return clusterName
}

// teardownCluster deletes a cluster and image registry.
func teardownCluster(clusterName string) {
	ch := NewClusterHelper()

	ch.EnsureDeleted(clusterName)

	TeardownImageRegistry()
}

// --- Endpoint helpers ---

// parseEngineArgs parses a comma-separated "k=v,k2=v2" string into a structured
// slice for direct injection into the endpoint template via {{range}}.
func parseEngineArgs(raw string) []EngineArg {
	if raw == "" {
		return nil
	}

	var args []EngineArg

	for _, pair := range splitEngineArgPairs(raw) {
		k, v, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok || k == "" {
			continue
		}

		args = append(args, EngineArg{Key: strings.TrimSpace(k), Value: strings.TrimSpace(v)})
	}

	return args
}

func splitEngineArgPairs(raw string) []string {
	var pairs []string
	start := 0
	depth := 0
	inString := false
	escaped := false

	for i, r := range raw {
		if escaped {
			escaped = false
			continue
		}

		if inString {
			if r == '\\' {
				escaped = true
				continue
			}

			if r == '"' {
				inString = false
			}

			continue
		}

		switch r {
		case '"':
			inString = true
		case '{', '[':
			depth++
		case '}', ']':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				pairs = append(pairs, raw[start:i])
				start = i + 1
			}
		}
	}

	pairs = append(pairs, raw[start:])

	return pairs
}

// engineArgs parses the named engine's profile engine_args.
func engineArgs(engineName string) []EngineArg {
	return parseEngineArgs(profileEngineArgsFor(engineName))
}

// mergeEngineArgs overlays model-level engine args onto engine-level defaults.
// Existing keys keep their position; model-level values win on duplicate keys.
func mergeEngineArgs(base, overlay []EngineArg) []EngineArg {
	if len(base) == 0 {
		return append([]EngineArg(nil), overlay...)
	}

	merged := append([]EngineArg(nil), base...)

	indexByKey := make(map[string]int, len(merged))
	for i, arg := range merged {
		indexByKey[arg.Key] = i
	}

	for _, arg := range overlay {
		if arg.Key == "" {
			continue
		}

		if i, ok := indexByKey[arg.Key]; ok {
			merged[i] = arg
			continue
		}

		indexByKey[arg.Key] = len(merged)
		merged = append(merged, arg)
	}

	return merged
}

func defaultEndpointEngineArgs(engineName, task string) []EngineArg {
	return mergeEngineArgs(
		engineArgs(engineName),
		parseEngineArgs(profileModelEngineArgsFor(task)),
	)
}

// endpointOpts holds configurable fields for applyEndpoint.
type endpointOpts struct {
	engineName        string
	engineVersion     string
	model             string
	modelVersion      string
	task              string
	engineArgs        []EngineArg
	gpu               string
	cpu               string
	memory            string
	accType           string
	accProduct        string
	vgpuMemoryMiB     string
	vgpuMemoryPercent string
	vgpuCorePercent   string
	env               map[string]string
	forceUpdate       bool
	replicas          int
}

// EndpointOption configures a single field of endpointOpts.
type EndpointOption func(*endpointOpts)

func withEngineVersion(v string) EndpointOption {
	return func(o *endpointOpts) { o.engineVersion = v }
}

func withEngine(name, version string) EndpointOption {
	return func(o *endpointOpts) {
		o.engineName = name
		o.engineVersion = version
	}
}

func withAccelerator(accType, accProduct string) EndpointOption {
	return func(o *endpointOpts) {
		o.accType = accType
		o.accProduct = accProduct
	}
}

func withModel(name, version string) EndpointOption {
	return func(o *endpointOpts) {
		o.model = name
		o.modelVersion = version
	}
}

func withTask(task string) EndpointOption {
	return func(o *endpointOpts) { o.task = task }
}

func withEngineArgs(args []EngineArg) EndpointOption {
	return func(o *endpointOpts) { o.engineArgs = args }
}

func withGPU(n string) EndpointOption {
	return func(o *endpointOpts) { o.gpu = n }
}

func withAcceleratorVirtualization(memoryMiB, memoryPercent, corePercent string) EndpointOption {
	return func(o *endpointOpts) {
		o.vgpuMemoryMiB = memoryMiB
		o.vgpuMemoryPercent = memoryPercent
		o.vgpuCorePercent = corePercent
	}
}

func withCPU(cpu string) EndpointOption {
	return func(o *endpointOpts) { o.cpu = cpu }
}

func withMemory(memory string) EndpointOption {
	return func(o *endpointOpts) { o.memory = memory }
}

func withEnv(env map[string]string) EndpointOption {
	return func(o *endpointOpts) { o.env = env }
}

func withoutForceUpdate() EndpointOption {
	return func(o *endpointOpts) { o.forceUpdate = false }
}

// withReplicas overrides the default spec.replicas.num (which is 1 in the
// template). Use withReplicas(0) to apply a paused endpoint.
func withReplicas(num int) EndpointOption {
	return func(o *endpointOpts) { o.replicas = num }
}

// renderEndpoint renders the endpoint YAML template and returns the temp file path and resolved options.
func renderEndpoint(name, cluster string, opts ...EndpointOption) (string, *endpointOpts) {
	o := &endpointOpts{
		engineName:    profileEngineName(),
		engineVersion: profileEngineVersion(),
		model:         profileModelName(),
		modelVersion:  profileModelVersion(),
		task:          profileModelTask(),
		// engineArgs intentionally left nil here. Resolved AFTER opts run
		// so withEngine can change engineName first and the per-engine
		// profile lookup picks the right engine's args.
		gpu:         "1",
		forceUpdate: true,
		replicas:    1,
	}
	for _, fn := range opts {
		fn(o)
	}

	if len(o.engineArgs) == 0 {
		o.engineArgs = defaultEndpointEngineArgs(o.engineName, o.task)
	}

	if o.accType == "" || o.accProduct == "" {
		o.accType, o.accProduct = getClusterAccelerator(cluster)
	}

	data := map[string]any{
		"E2E_ENDPOINT_NAME":                             name,
		"E2E_WORKSPACE":                                 profileWorkspace(),
		"E2E_CLUSTER_NAME":                              cluster,
		"E2E_ENGINE_NAME":                               o.engineName,
		"E2E_ENGINE_VERSION":                            o.engineVersion,
		"E2E_MODEL_REGISTRY":                            testRegistry(),
		"E2E_MODEL_NAME":                                o.model,
		"E2E_MODEL_VERSION":                             o.modelVersion,
		"E2E_MODEL_TASK":                                o.task,
		"E2E_ACCELERATOR_TYPE":                          o.accType,
		"E2E_ACCELERATOR_PRODUCT":                       o.accProduct,
		"E2E_ACCELERATOR_VIRTUALIZATION_MEMORY_MIB":     o.vgpuMemoryMiB,
		"E2E_ACCELERATOR_VIRTUALIZATION_MEMORY_PERCENT": o.vgpuMemoryPercent,
		"E2E_ACCELERATOR_VIRTUALIZATION_CORE_PERCENT":   o.vgpuCorePercent,
		"E2E_GPU":                                       o.gpu,
		"E2E_CPU":                                       o.cpu,
		"E2E_MEMORY":                                    o.memory,
		"E2E_ENGINE_ARGS":                               o.engineArgs,
		"E2E_ENV":                                       o.env,
		"E2E_REPLICAS_NUM":                              o.replicas,
	}

	yamlPath, err := renderTemplateToTempFile(filepath.Join("testdata", "endpoint.yaml"), data)
	Expect(err).NotTo(HaveOccurred(), "failed to render endpoint template")

	return yamlPath, o
}

// applyEndpoint renders and applies an endpoint on a cluster.
func applyEndpoint(name, cluster string, opts ...EndpointOption) (yamlPath string) {
	yamlPath, o := renderEndpoint(name, cluster, opts...)

	args := []string{"apply", "-f", yamlPath}
	if o.forceUpdate {
		args = append(args, "--force-update")
	}

	r := RunCLI(args...)
	ExpectSuccess(r)

	return yamlPath
}

// allSchemaTypesEngineArgs returns an engine_args slice covering multiple JSON
// Schema data types. enforce_eager covers the true-bool path (flag emitted with
// no value); enable_prefix_caching covers the false-bool path (flag dropped
// entirely, since vLLM argparse rejects `--flag false` for store_true flags).
func allSchemaTypesEngineArgs() []EngineArg {
	return []EngineArg{
		{Key: "dtype", Value: "half"},
		{Key: "max_model_len", Value: "4096"},
		{Key: "gpu_memory_utilization", Value: "0.85"},
		{Key: "enforce_eager", Value: "true"},
		{Key: "enable_prefix_caching", Value: "false"},
		{Key: "seed", Value: "42"},
		{Key: "served_model_name", Value: fmt.Sprintf(`["%s","neu-vllm-list-alias"]`, profileModelName())},
		{Key: "override_generation_config", Value: `'{"temperature": 0.8}'`},
	}
}

// allSchemaTypesEngineArgsSGLang returns an engine_args slice for the SGLang
// engine covering int / float / bool / string / string-enum / object /
// nested-object. Keys use ServerArgs dataclass field names (underscore form);
// the K8s template applies sprig replace "_" "-" so the CLI side sees kebab.
// Array types are intentionally absent — SGLang's only array-shaped CLI flag
// is `--cuda-graph-bs` (nargs="+"), which the single-token K8s template can't
// render.
//
// disable_cuda_graph covers the true-bool path (flag emitted with no value);
// skip_tokenizer_init covers the false-bool path (flag dropped entirely,
// since SGLang argparse rejects `--flag false` for store_true flags).
func allSchemaTypesEngineArgsSGLang() []EngineArg {
	return []EngineArg{
		{Key: "tp_size", Value: "1"},
		{Key: "mem_fraction_static", Value: "0.85"},
		{Key: "disable_cuda_graph", Value: "true"},
		{Key: "skip_tokenizer_init", Value: "false"},
		{Key: "dtype", Value: "auto"},
		{Key: "chunked_prefill_size", Value: "8192"},
		{Key: "served_model_name", Value: "neu-sglang-test"},
		{Key: "attention_backend", Value: "torch_native"},
		{Key: "cuda_graph_max_bs", Value: "4"},
		{Key: "preferred_sampling_params", Value: `'{"temperature": 0.7, "top_p": 0.9}'`},
		{Key: "json_model_override_args", Value: `'{"max_position_embeddings": 32768}'`},
	}
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

// waitEndpointPaused waits for an endpoint to reach Paused phase.
func waitEndpointPaused(name string) {
	r := RunCLI("wait", "endpoint", name,
		"-w", profileWorkspace(),
		"--for", "jsonpath=.status.phase=Paused",
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
	r := RunCLI("delete", "endpoint", name, "-w", profileWorkspace(), "--force", "--ignore-not-found")
	ExpectSuccess(r)

	r = RunCLI("wait", "endpoint", name,
		"-w", profileWorkspace(),
		"--for", "delete",
		"--timeout", "5m",
	)
	ExpectSuccess(r)
}

func parseEndpointJSON(stdout string) v1.Endpoint {
	var ep v1.Endpoint
	ExpectWithOffset(1, json.Unmarshal([]byte(stdout), &ep)).To(Succeed())

	return ep
}

// --- Inference helpers ---

// doInferenceRequest sends a JSON POST to the given URL path and returns (status_code, body, error).
func doInferenceRequest(serviceURL, path string, reqBody map[string]any) (int, string, error) {
	payloadBytes, err := json.Marshal(reqBody)
	if err != nil {
		return 0, "", err
	}

	client := &http.Client{Timeout: 60 * time.Second}

	req, err := http.NewRequest(http.MethodPost,
		strings.TrimRight(serviceURL, "/")+path,
		strings.NewReader(string(payloadBytes)),
	)
	if err != nil {
		return 0, "", err
	}

	req.Header.Set("Content-Type", "application/json")

	authValue := Cfg.APIKey
	if !strings.HasPrefix(authValue, "Bearer ") {
		authValue = "Bearer " + authValue
	}

	req.Header.Set("Authorization", authValue)

	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, "", err
	}

	return resp.StatusCode, string(body), nil
}

func inferChat(serviceURL, prompt string) (int, string, error) {
	return inferChatWithModel(serviceURL, profileModelName(), prompt)
}

func inferChatWithModel(serviceURL, model, prompt string) (int, string, error) {
	return doInferenceRequest(serviceURL, "/v1/chat/completions", map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"max_tokens": 16,
	})
}

//nolint:unparam // input is a fixture-style literal across callers by design;
func inferEmbedding(serviceURL, model, input string) (int, string, error) {
	return doInferenceRequest(serviceURL, "/v1/embeddings", map[string]any{
		"model": model,
		"input": input,
	})
}

//nolint:unparam // query is a fixture-style literal across callers by design;
func inferRerank(serviceURL, model, query string, documents []string) (int, string, error) {
	return doInferenceRequest(serviceURL, "/v1/rerank", map[string]any{
		"model":     model,
		"query":     query,
		"documents": documents,
	})
}
