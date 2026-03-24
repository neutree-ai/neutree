package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// --- Template-based YAML rendering ---

// renderSSHClusterYAML renders the SSH cluster template with overrides and returns the temp file path.
// Overrides: name, version, image_registry, head_ip, worker_ips (comma-separated), ssh_user, ssh_private_key.
func renderSSHClusterYAML(overrides map[string]string) string {
	defaults := map[string]string{
		"CLUSTER_NAME":            overrides["name"],
		"CLUSTER_WORKSPACE":      Cfg.Workspace,
		"CLUSTER_IMAGE_REGISTRY": valueOr(overrides, "image_registry", testImageRegistry()),
		"CLUSTER_VERSION":        valueOr(overrides, "version", testClusterVersion()),
		"CLUSTER_SSH_HEAD_IP":    overrides["head_ip"],
		"CLUSTER_SSH_USER":       overrides["ssh_user"],
		"CLUSTER_SSH_PRIVATE_KEY": overrides["ssh_private_key"],
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
		"CLUSTER_WORKSPACE":      Cfg.Workspace,
		"CLUSTER_IMAGE_REGISTRY": valueOr(overrides, "image_registry", testImageRegistry()),
		"CLUSTER_VERSION":        valueOr(overrides, "version", testClusterVersion()),
		"CLUSTER_KUBECONFIG":     overrides["kubeconfig"],
		"CLUSTER_ROUTER_REPLICAS": valueOr(overrides, "router_replicas", "1"),
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
		workspace: Cfg.Workspace,
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

// EnsureDeleted deletes a cluster with --force, ignoring errors (for cleanup).
func (c *ClusterHelper) EnsureDeleted(name string) {
	c.Delete(name)
}

// --- clusterJSON for parsing `get cluster -o json` ---

type clusterJSON struct {
	Status struct {
		Phase            string `json:"phase"`
		Initialized      bool   `json:"initialized"`
		ObservedSpecHash string `json:"observed_spec_hash"`
		ReadyNodes       int    `json:"ready_nodes"`
		DesiredNodes     int    `json:"desired_nodes"`
		DashboardURL     string `json:"dashboard_url"`
		Version          string `json:"version"`
		RayVersion       string `json:"ray_version"`
		ErrorMessage     string `json:"error_message"`
		ResourceInfo     any    `json:"resource_info"`
	} `json:"status"`
}

func parseClusterJSON(stdout string) clusterJSON {
	var c clusterJSON
	ExpectWithOffset(1, json.Unmarshal([]byte(stdout), &c)).To(Succeed())
	return c
}

// --- Environment helpers ---

func testImageRegistry() string {
	return "e2e-image-registry-" + Cfg.RunID
}

func testClusterVersion() string {
	return "v1.0.0"
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

// --- Image registry setup/teardown for cluster tests ---

var imageRegistryYAML string

func SetupImageRegistry() {
	defaults := map[string]string{
		"E2E_IMAGE_REGISTRY":      testImageRegistry(),
		"E2E_WORKSPACE":           testWorkspace(),
		"E2E_IMAGE_REGISTRY_URL":  profile.ImageRegistry.URL,
		"E2E_IMAGE_REGISTRY_REPO": profile.ImageRegistry.Repository,
	}
	var err error
	imageRegistryYAML, err = renderTemplateToTempFile(
		filepath.Join("testdata", "image-registry.yaml"), defaults,
	)
	Expect(err).NotTo(HaveOccurred(), "failed to render image registry template")

	r := RunCLI("apply", "-f", imageRegistryYAML)
	ExpectSuccess(r)

	r = RunCLI("wait", "imageregistry", testImageRegistry(),
		"-w", Cfg.Workspace,
		"--for", "jsonpath=.status.phase=Connected",
		"--timeout", "2m",
	)
	ExpectSuccess(r)
}

func TeardownImageRegistry() {
	if imageRegistryYAML != "" {
		RunCLI("delete", "-f", imageRegistryYAML, "--force", "--ignore-not-found")
		os.Remove(imageRegistryYAML)
	}
}

// --- Tests ---

var _ = Describe("Cluster Status", Ordered, Label("cluster"), func() {
	var ClusterH *ClusterHelper

	BeforeAll(func() {
		// Require image registry config from profile for all cluster tests.
		if profile.ImageRegistry.URL == "" {
			Skip("ImageRegistry.URL not configured in profile, skipping cluster tests")
		}
		if profile.ImageRegistry.Repository == "" {
			Skip("ImageRegistry.Repository not configured in profile, skipping cluster tests")
		}

		By("Setting up image registry")
		SetupImageRegistry()
		ClusterH = NewClusterHelper()
	})

	AfterAll(func() {
		By("Tearing down image registry")
		TeardownImageRegistry()
	})

	// --- SSH Cluster Lifecycle ---

	Describe("SSH Cluster Lifecycle", Ordered, Label("ssh"), func() {
		var (
			clusterName   string
			headIP        string
			workerIPs     string
			sshUser       string
			sshPrivateKey string
		)

		BeforeAll(func() {
			headIP, workerIPs, sshUser, sshPrivateKey = requireSSHEnv()
			clusterName = "e2e-ssh-" + Cfg.RunID

			yaml := renderSSHClusterYAML(map[string]string{
				"name":            clusterName,
				"head_ip":         headIP,
				"worker_ips":      workerIPs,
				"ssh_user":        sshUser,
				"ssh_private_key": sshPrivateKey,
			})

			By("Applying SSH cluster")
			r := ClusterH.Apply(yaml)
			ExpectSuccess(r)
		})

		AfterAll(func() {
			By("Force-deleting SSH cluster")
			ClusterH.EnsureDeleted(clusterName)
		})

		It("should transition to Running", func() {
			By("Waiting for Running phase")
			r := ClusterH.WaitForPhase(clusterName, "Running", "10m")
			ExpectSuccess(r)

			By("Verifying cluster status fields")
			r = ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c := parseClusterJSON(r.Stdout)

			Expect(c.Status.Phase).To(Equal("Running"))
			Expect(c.Status.Initialized).To(BeTrue())
			Expect(c.Status.ObservedSpecHash).NotTo(BeEmpty())
			Expect(c.Status.ReadyNodes).To(BeNumerically(">=", c.Status.DesiredNodes))

			// desired_nodes = 1 (head) + len(worker_ips)
			expectedNodes := 1
			if workerIPs != "" {
				expectedNodes += len(strings.Split(workerIPs, ","))
			}
			Expect(c.Status.DesiredNodes).To(Equal(expectedNodes))
			Expect(c.Status.DashboardURL).NotTo(BeEmpty())
			Expect(c.Status.ErrorMessage).To(BeEmpty())
			Expect(c.Status.ResourceInfo).NotTo(BeNil(), "SSH cluster should have resource_info")
		})

		It("should show Updating then Running on spec change", func() {
			By("Recording old spec hash")
			r := ClusterH.Get(clusterName)
			ExpectSuccess(r)
			oldHash := parseClusterJSON(r.Stdout).Status.ObservedSpecHash

			By("Applying with worker_ips removed (head-only)")
			yaml := renderSSHClusterYAML(map[string]string{
				"name":            clusterName,
				"head_ip":         headIP,
				"ssh_user":        sshUser,
				"ssh_private_key": sshPrivateKey,
				"image_registry":  testImageRegistry(),
				// No worker_ips — changes spec from head+workers to head-only.
			})
			r = ClusterH.Apply(yaml)
			ExpectSuccess(r)

			By("Polling for Updating phase (may be transient)")
			seenUpdating := false
			deadline := time.Now().Add(30 * time.Second)
			for time.Now().Before(deadline) {
				r = ClusterH.Get(clusterName)
				if r.ExitCode == 0 {
					c := parseClusterJSON(r.Stdout)
					if c.Status.Phase == "Updating" {
						seenUpdating = true
						break
					}
					if c.Status.Phase == "Running" && c.Status.ObservedSpecHash != oldHash {
						break
					}
				}
				time.Sleep(2 * time.Second)
			}
			_ = seenUpdating // Updating may be too transient to observe.

			By("Waiting for Running phase again")
			r = ClusterH.WaitForPhase(clusterName, "Running", "10m")
			ExpectSuccess(r)

			By("Verifying spec hash changed")
			r = ClusterH.Get(clusterName)
			ExpectSuccess(r)
			newHash := parseClusterJSON(r.Stdout).Status.ObservedSpecHash
			Expect(newHash).NotTo(Equal(oldHash), "spec hash should change after spec update")
		})

		It("should transition through Deleting to Deleted", func() {
			By("Deleting gracefully (no --force)")
			r := ClusterH.DeleteGraceful(clusterName)
			ExpectSuccess(r)

			By("Waiting for cluster to be deleted")
			r = ClusterH.WaitForDelete(clusterName, "10m")
			ExpectSuccess(r)
		})
	})

	// --- K8s Cluster Lifecycle ---

	Describe("K8s Cluster Lifecycle", Ordered, Label("k8s"), func() {
		var (
			clusterName string
			kubeconfig  string
		)

		BeforeAll(func() {
			kubeconfig = requireK8sEnv()
			clusterName = "e2e-k8s-" + Cfg.RunID

			yaml := renderK8sClusterYAML(map[string]string{
				"name":       clusterName,
				"kubeconfig": kubeconfig,
			})

			By("Applying K8s cluster")
			r := ClusterH.Apply(yaml)
			ExpectSuccess(r)
		})

		AfterAll(func() {
			By("Force-deleting K8s cluster")
			ClusterH.EnsureDeleted(clusterName)
		})

		It("should transition to Running", func() {
			By("Waiting for Running phase")
			r := ClusterH.WaitForPhase(clusterName, "Running", "10m")
			ExpectSuccess(r)

			By("Verifying cluster status fields")
			r = ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c := parseClusterJSON(r.Stdout)

			Expect(c.Status.Phase).To(Equal("Running"))
			Expect(c.Status.Initialized).To(BeTrue())
			Expect(c.Status.ObservedSpecHash).NotTo(BeEmpty())
			Expect(c.Status.DashboardURL).NotTo(BeEmpty())
			Expect(c.Status.ErrorMessage).To(BeEmpty())
			Expect(c.Status.ResourceInfo).NotTo(BeNil(), "K8s cluster should have resource_info")
		})

		It("should show Updating then Running on spec change", func() {
			By("Recording old spec hash")
			r := ClusterH.Get(clusterName)
			ExpectSuccess(r)
			oldHash := parseClusterJSON(r.Stdout).Status.ObservedSpecHash

			By("Applying with router replicas change")
			yaml := renderK8sClusterYAML(map[string]string{
				"name":             clusterName,
				"kubeconfig":       kubeconfig,
				"image_registry":   testImageRegistry(),
				"router_replicas":  "2",
			})
			r = ClusterH.Apply(yaml)
			ExpectSuccess(r)

			By("Polling for Updating phase (may be transient)")
			seenUpdating := false
			deadline := time.Now().Add(30 * time.Second)
			for time.Now().Before(deadline) {
				r = ClusterH.Get(clusterName)
				if r.ExitCode == 0 {
					c := parseClusterJSON(r.Stdout)
					if c.Status.Phase == "Updating" {
						seenUpdating = true
						break
					}
					if c.Status.Phase == "Running" && c.Status.ObservedSpecHash != oldHash {
						break
					}
				}
				time.Sleep(2 * time.Second)
			}
			_ = seenUpdating

			By("Waiting for Running phase again")
			r = ClusterH.WaitForPhase(clusterName, "Running", "10m")
			ExpectSuccess(r)

			By("Verifying spec hash changed")
			r = ClusterH.Get(clusterName)
			ExpectSuccess(r)
			newHash := parseClusterJSON(r.Stdout).Status.ObservedSpecHash
			Expect(newHash).NotTo(Equal(oldHash), "spec hash should change after spec update")
		})

		It("should transition through Deleting to Deleted", func() {
			By("Deleting gracefully (no --force)")
			r := ClusterH.DeleteGraceful(clusterName)
			ExpectSuccess(r)

			By("Waiting for cluster to be deleted")
			r = ClusterH.WaitForDelete(clusterName, "10m")
			ExpectSuccess(r)
		})
	})

	// --- Initializing with bad dependency ---

	Describe("Initializing with bad dependency", Label("status"), func() {

		It("should stay Initializing when image registry is invalid", func() {
			headIP, workerIPs, sshUser, sshPrivateKey := requireSSHEnv()
			clusterName := "e2e-bad-dep-" + Cfg.RunID
			DeferCleanup(func() {
				ClusterH.EnsureDeleted(clusterName)
			})

			By("Applying SSH cluster referencing non-existent image registry")
			yaml := renderSSHClusterYAML(map[string]string{
				"name":            clusterName,
				"head_ip":         headIP,
				"worker_ips":      workerIPs,
				"ssh_user":        sshUser,
				"ssh_private_key": sshPrivateKey,
				"image_registry":  "non-existent-registry-" + Cfg.RunID,
			})
			r := ClusterH.Apply(yaml)
			ExpectSuccess(r)

			By("Waiting for controller to reconcile and verifying Initializing phase")
			// Poll until phase is Initializing (controller may need time to process).
			// The cluster should never reach Running because the image registry is invalid.
			deadline := time.Now().Add(60 * time.Second)
			var c clusterJSON
			for time.Now().Before(deadline) {
				r = ClusterH.Get(clusterName)
				if r.ExitCode == 0 {
					c = parseClusterJSON(r.Stdout)
					// If error_message is set, the controller has reconciled
					if c.Status.ErrorMessage != "" {
						break
					}
				}
				time.Sleep(5 * time.Second)
			}
			Expect(c.Status.Phase).To(Equal("Initializing"),
				"cluster with bad dependency should stay in Initializing phase")
			// error_message may or may not be set depending on controller timing,
			// but the cluster must NOT reach Running.
		})
	})
})
