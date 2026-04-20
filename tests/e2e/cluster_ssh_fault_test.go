package e2e

import (
	"encoding/base64"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

var _ = Describe("SSH Cluster Fault & Anomaly", Ordered, Label("cluster", "ssh"), func() {
	var ClusterH *ClusterHelper

	BeforeAll(func() {
		requireImageRegistryEnv()

		By("Setting up image registry")
		SetupImageRegistry()
		ClusterH = NewClusterHelper()
	})

	AfterAll(func() {
		TeardownImageRegistry()
	})

	// --- Fault Recovery ---

	Describe("Fault Recovery", Ordered, Label("fault"), func() {
		var (
			clusterName   string
			headIP        string
			workerIPs     string
			sshUser       string
			sshPrivateKey string
			sshKeyFile    string
		)

		BeforeAll(func() {
			headIP, workerIPs, sshUser, sshPrivateKey = requireSSHEnv()
			if len(profile.SSHNodes) == 0 || profile.SSHNodes[0].KeyFile == "" {
				Skip("SSH key file path not configured in profile")
			}
			sshKeyFile = expandHome(profile.SSHNodes[0].KeyFile)
			clusterName = "e2e-ssh-fault-" + Cfg.RunID

			yaml := renderSSHClusterYAML(map[string]string{
				"name":            clusterName,
				"head_ip":         headIP,
				"worker_ips":      workerIPs,
				"ssh_user":        sshUser,
				"ssh_private_key": sshPrivateKey,
			})

			r := ClusterH.Apply(yaml)
			ExpectSuccess(r)

			r = ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
			ExpectSuccess(r)
		})

		AfterAll(func() {
			ClusterH.EnsureDeleted(clusterName)
		})

		It("should recover after head raylet is killed", Label("C2644066"), func() {
			r := RunSSH(sshUser, headIP, sshKeyFile,
				"nohup docker exec ray_container pkill -f 'dist-packages/ray/core/src/ray/raylet/raylet' > /dev/null 2>&1 &")
			ExpectSuccess(r)

			r = ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
			ExpectSuccess(r)

			r = ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c := parseClusterJSON(r.Stdout)
			Expect(c.Status.ReadyNodes).To(Equal(c.Status.DesiredNodes))
		})

		It("should enter Failed and auto-recover after head Ray processes are stopped", Label("C2644067", "C2613102"), func() {
			r := RunSSH(sshUser, headIP, sshKeyFile, "docker exec ray_container ray stop --force || true")
			ExpectSuccess(r)

			By("Waiting for cluster to enter Failed phase")
			ClusterH.WaitForClusterFailed(clusterName, IntermediatePhaseTimeout)

			By("Waiting for auto-recovery to Running phase")
			r = ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
			ExpectSuccess(r)

			r = ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c := parseClusterJSON(r.Stdout)
			Expect(c.Status.ReadyNodes).To(Equal(c.Status.DesiredNodes))
		})
	})

	// --- Configuration Anomaly ---

	Describe("Configuration Anomaly", Label("error"), func() {

		It("should stay Initializing when SSH private key is invalid", Label("C2613143"), func() {
			headIP := profileSSHHeadIP()
			if headIP == "" {
				Skip("SSH head IP not configured")
			}

			clusterName := "e2e-ssh-badkey-" + Cfg.RunID
			DeferCleanup(func() { ClusterH.EnsureDeleted(clusterName) })

			yaml := renderSSHClusterYAML(map[string]string{
				"name":            clusterName,
				"head_ip":         headIP,
				"ssh_user":        "root",
				"ssh_private_key": base64.StdEncoding.EncodeToString([]byte("invalid-ssh-key-content")),
			})
			r := ClusterH.Apply(yaml)
			ExpectSuccess(r)

			ClusterH.EventuallyInPhase(clusterName, v1.ClusterPhaseInitializing, "connection failed", IntermediatePhaseTimeout)
		})

		It("should stay Initializing when head node IP is unreachable", Label("C2613145"), func() {
			_, _, sshUser, sshPrivateKey := requireSSHEnv()

			clusterName := "e2e-ssh-badip-" + Cfg.RunID
			DeferCleanup(func() { ClusterH.EnsureDeleted(clusterName) })

			yaml := renderSSHClusterYAML(map[string]string{
				"name":            clusterName,
				"head_ip":         "198.51.100.1",
				"ssh_user":        sshUser,
				"ssh_private_key": sshPrivateKey,
			})
			r := ClusterH.Apply(yaml)
			ExpectSuccess(r)

			ClusterH.EventuallyInPhase(clusterName, v1.ClusterPhaseInitializing, "connection failed", IntermediatePhaseTimeout)
		})

		It("should stay Initializing when worker node IPs are unreachable", Label("C2613146"), func() {
			headIP, _, sshUser, sshPrivateKey := requireSSHEnv()

			clusterName := "e2e-ssh-badwkr-" + Cfg.RunID
			DeferCleanup(func() { ClusterH.EnsureDeleted(clusterName) })

			yaml := renderSSHClusterYAML(map[string]string{
				"name":            clusterName,
				"head_ip":         headIP,
				"worker_ips":      "198.51.100.2,198.51.100.3",
				"ssh_user":        sshUser,
				"ssh_private_key": sshPrivateKey,
			})
			r := ClusterH.Apply(yaml)
			ExpectSuccess(r)

			ClusterH.EventuallyInPhase(clusterName, v1.ClusterPhaseInitializing, "connection failed", IntermediatePhaseTimeout)
		})

		It("should stay Initializing when image registry is unreachable from nodes", Label("C2613149"), func() {
			headIP, _, sshUser, sshPrivateKey := requireSSHEnv()

			clusterName := "e2e-ssh-badreg-" + Cfg.RunID
			DeferCleanup(func() { ClusterH.EnsureDeleted(clusterName) })

			badRegistryName := "e2e-bad-registry-" + Cfg.RunID
			defaults := map[string]string{
				"E2E_IMAGE_REGISTRY":      badRegistryName,
				"E2E_WORKSPACE":           profileWorkspace(),
				"E2E_IMAGE_REGISTRY_URL":  "198.51.100.99:5000",
				"E2E_IMAGE_REGISTRY_REPO": "nonexistent",
			}

			yamlPath, err := renderTemplateToTempFile("testdata/image-registry.yaml", defaults)
			Expect(err).NotTo(HaveOccurred())

			r := RunCLI("apply", "-f", yamlPath)
			ExpectSuccess(r)
			DeferCleanup(func() {
				RunCLI("delete", "-f", yamlPath, "--force", "--ignore-not-found")
			})

			yaml := renderSSHClusterYAML(map[string]string{
				"name":            clusterName,
				"head_ip":         headIP,
				"ssh_user":        sshUser,
				"ssh_private_key": sshPrivateKey,
				"image_registry":  badRegistryName,
			})
			r = ClusterH.Apply(yaml)
			ExpectSuccess(r)

			ClusterH.EventuallyInPhase(clusterName, v1.ClusterPhaseInitializing, "not ready", IntermediatePhaseTimeout)
		})

		It("should stay Initializing when image registry reference is invalid", Label("C2612657"), func() {
			headIP, workerIPs, sshUser, sshPrivateKey := requireSSHEnv()

			clusterName := "e2e-bad-dep-" + Cfg.RunID
			DeferCleanup(func() { ClusterH.EnsureDeleted(clusterName) })

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

			ClusterH.EventuallyInPhase(clusterName, v1.ClusterPhaseInitializing, "not found", IntermediatePhaseTimeout)
		})
	})
})

