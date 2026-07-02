package e2e

import (
	"fmt"
	"os"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

var _ = Describe("SSH Cluster Config", Ordered, Label("cluster", "ssh", "config"), func() {
	var ClusterH *ClusterHelper

	BeforeAll(func() {
		requireImageRegistryProfile()

		By("Setting up image registry")
		SetupImageRegistry()
		ClusterH = NewClusterHelper()
	})

	AfterAll(func() {
		TeardownImageRegistry()
	})

	// --- Create Verification ---

	Describe("Create Verification", Ordered, func() {
		var (
			clusterName   string
			headIP        string
			workerIPs     []string
			sshUser       string
			sshPrivateKey string
			sshKeyFile    string
		)

		BeforeAll(func() {
			headIP, workerIPs, sshUser, sshPrivateKey = requireSSHProfile()
			if len(profile.SSHNodes) == 0 || profile.SSHNodes[0].KeyFile == "" {
				Skip("SSH key file path not configured in profile")
			}
			sshKeyFile = expandHome(profile.SSHNodes[0].KeyFile)
			clusterName = "e2e-ssh-verify-" + Cfg.RunID

			overrides := map[string]any{
				"name":            clusterName,
				"head_ip":         headIP,
				"worker_ips":      workerIPs,
				"ssh_user":        sshUser,
				"ssh_private_key": sshPrivateKey,
			}

			if profile.ModelCache.HostPath != "" {
				overrides["model_caches"] = []ModelCache{
					{Name: "hp-cache", Mode: "host_path", HostPath: profile.ModelCache.HostPath},
				}
			}

			yaml := renderSSHClusterYAML(overrides)

			r := ClusterH.Apply(yaml)
			ExpectSuccess(r)

			r = ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
			ExpectSuccess(r)
		})

		AfterAll(func() {
			ClusterH.EnsureDeleted(clusterName)
		})

		It("should have ray_container running on all nodes", Label("C2613077"), func() {
			r := RunSSH(sshUser, headIP, sshKeyFile,
				rayContainerNameCommand(clusterName, v1.StaticNodeRoleHead))
			ExpectSuccess(r)
			Expect(strings.TrimSpace(r.Stdout)).NotTo(BeEmpty())

			for _, ip := range workerIPs {
				r = RunSSH(sshUser, ip, sshKeyFile,
					rayContainerNameCommand(clusterName, v1.StaticNodeRoleWorker))
				ExpectSuccess(r)
				Expect(strings.TrimSpace(r.Stdout)).NotTo(BeEmpty(),
					"Ray worker container should be running on worker %s", ip)
			}
		})

		It("should have correct HostPath permissions on all nodes", Label("C2613078"), func() {
			if usesStaticNodeClusterFlow(profileClusterVersion()) {
				Skip("static node cluster flow does not manage legacy SSH model cache host paths")
			}

			cachePath := profile.ModelCache.HostPath
			if cachePath == "" {
				cachePath = "/opt/neutree/model-cache"
			}

			nodes := append([]string{headIP}, workerIPs...)

			for _, ip := range nodes {
				r := RunSSH(sshUser, ip, sshKeyFile,
					fmt.Sprintf("stat -c '%%a %%u %%g' %s 2>/dev/null || echo 'NOT_FOUND'", cachePath))
				ExpectSuccess(r)
				Expect(r.Stdout).NotTo(ContainSubstring("NOT_FOUND"),
					"model cache directory %s should exist on node %s", cachePath, ip)

				fields := strings.Fields(strings.TrimSpace(r.Stdout))
				Expect(fields).To(HaveLen(3),
					"stat output on node %s should be '<mode> <uid> <gid>', got: %s", ip, r.Stdout)
				Expect(fields[0]).To(Equal("755"),
					"model cache directory on node %s should have mode 755", ip)
				Expect(fields[1]).To(Equal("0"),
					"model cache directory on node %s should be owned by root (uid 0)", ip)
				Expect(fields[2]).To(Equal("0"),
					"model cache directory on node %s should be owned by root group (gid 0)", ip)
			}
		})

		It("should clean up ray_container after deletion", Label("C2612850"), func() {
			r := ClusterH.DeleteGraceful(clusterName)
			ExpectSuccess(r)

			r = ClusterH.WaitForDelete(clusterName, TerminalPhaseTimeout)
			ExpectSuccess(r)

			r = RunSSH(sshUser, headIP, sshKeyFile,
				rayContainerListCommand(clusterName, v1.StaticNodeRoleHead, true))
			ExpectSuccess(r)
			Expect(strings.TrimSpace(r.Stdout)).To(BeEmpty())

			for _, ip := range workerIPs {
				r = RunSSH(sshUser, ip, sshKeyFile,
					rayContainerListCommand(clusterName, v1.StaticNodeRoleWorker, true))
				ExpectSuccess(r)
				Expect(strings.TrimSpace(r.Stdout)).To(BeEmpty(),
					"Ray worker container should be cleaned up from worker %s", ip)
			}
		})

		It("should preserve model cache after deletion", Label("C2613100"), func() {
			if usesStaticNodeClusterFlow(profileClusterVersion()) {
				Skip("static node cluster flow does not manage legacy SSH model cache host paths")
			}

			cachePath := profile.ModelCache.HostPath
			if cachePath == "" {
				cachePath = "/opt/neutree/model-cache"
			}

			r := RunSSH(sshUser, headIP, sshKeyFile,
				fmt.Sprintf("test -d %s && echo EXISTS || echo GONE", cachePath))
			ExpectSuccess(r)
			Expect(r.Stdout).To(ContainSubstring("EXISTS"),
				"model cache directory %s should be preserved after cluster deletion", cachePath)
		})
	})

	// --- Worker Edit ---

	Describe("Worker Edit", Ordered, Label("edit"), func() {
		var (
			clusterName   string
			headIP        string
			workerIPs     []string
			sshUser       string
			sshPrivateKey string
		)

		BeforeAll(func() {
			headIP, workerIPs, sshUser, sshPrivateKey = requireSSHProfile()
			if len(workerIPs) == 0 {
				Skip("No worker IPs configured, skipping worker edit tests")
			}

			clusterName = "e2e-ssh-edit-" + Cfg.RunID

			yaml := renderSSHClusterYAML(map[string]any{
				"name":            clusterName,
				"head_ip":         headIP,
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

		It("should add worker node and reach Running", Label("C2612830"), func() {
			r := ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
			ExpectSuccess(r)

			r = ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c := parseClusterJSON(r.Stdout)
			oldNodes := c.Status.DesiredNodes

			yaml := renderSSHClusterYAML(map[string]any{
				"name":            clusterName,
				"head_ip":         headIP,
				"worker_ips":      workerIPs,
				"ssh_user":        sshUser,
				"ssh_private_key": sshPrivateKey,
			})
			r = ClusterH.Apply(yaml)
			ExpectSuccess(r)

			ClusterH.EventuallyInPhase(clusterName, v1.ClusterPhaseUpdating, "", IntermediatePhaseTimeout)

			r = ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
			ExpectSuccess(r)

			r = ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c = parseClusterJSON(r.Stdout)
			Expect(c.Status.DesiredNodes).To(BeNumerically(">", oldNodes))
			Expect(c.Status.ReadyNodes).To(Equal(c.Status.DesiredNodes))

			if usesStaticNodeClusterFlow(profileClusterVersion()) {
				expectedNodes := 1 + len(workerIPs)
				eventuallyStaticNodeClusterReady(clusterName, profileClusterVersion(), expectedNodes, TerminalPhaseTimeout)
				assertStaticNodesForCluster(clusterName, expectedStaticNodeIPs(headIP, workerIPs))
			}
		})

		It("should remove worker node and reach Running", Label("C2612831"), func() {
			r := ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
			ExpectSuccess(r)

			r = ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c := parseClusterJSON(r.Stdout)
			oldNodes := c.Status.DesiredNodes

			yaml := renderSSHClusterYAML(map[string]any{
				"name":            clusterName,
				"head_ip":         headIP,
				"ssh_user":        sshUser,
				"ssh_private_key": sshPrivateKey,
			})
			r = ClusterH.Apply(yaml)
			ExpectSuccess(r)

			ClusterH.EventuallyInPhase(clusterName, v1.ClusterPhaseUpdating, "", IntermediatePhaseTimeout)

			r = ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
			ExpectSuccess(r)

			r = ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c = parseClusterJSON(r.Stdout)
			Expect(c.Status.DesiredNodes).To(BeNumerically("<", oldNodes))
			Expect(c.Status.ReadyNodes).To(Equal(c.Status.DesiredNodes),
				"ReadyNodes should match DesiredNodes after worker removal")

			if usesStaticNodeClusterFlow(profileClusterVersion()) {
				eventuallyStaticNodeClusterReady(clusterName, profileClusterVersion(), 1, TerminalPhaseTimeout)
				assertStaticNodesForCluster(clusterName, []string{headIP})
			}
		})
	})

	// --- Model Cache Edit ---

	Describe("Model Cache Edit", Ordered, Label("edit", "endpoint"), func() {
		var (
			clusterName    string
			epName         string
			headIP         string
			workerIPs      []string
			sshUser        string
			sshPrivateKey  string
			sshKeyFile     string
			modelCachePath string
		)

		BeforeAll(func() {
			if profile.ModelCache.HostPath == "" {
				Skip("ModelCache.HostPath not configured in profile")
			}

			headIP, workerIPs, sshUser, sshPrivateKey = requireSSHProfile()
			if len(profile.SSHNodes) == 0 || profile.SSHNodes[0].KeyFile == "" {
				Skip("SSH key file path not configured in profile")
			}

			sshKeyFile = expandHome(profile.SSHNodes[0].KeyFile)
			clusterName = "e2e-ssh-mc-" + Cfg.RunID
			epName = "e2e-ssh-mc-ep-" + Cfg.RunID
			modelCachePath = "/tmp/neutree-e2e-model-cache-" + Cfg.RunID

			By("Setting up model registry")
			SetupModelRegistry()

			yaml := renderSSHClusterYAML(map[string]any{
				"name":            clusterName,
				"head_ip":         headIP,
				"worker_ips":      workerIPs,
				"ssh_user":        sshUser,
				"ssh_private_key": sshPrivateKey,
				"model_caches": []ModelCache{
					{Name: "test-cache", Mode: "host_path", HostPath: modelCachePath},
				},
			})

			r := ClusterH.Apply(yaml)
			ExpectSuccess(r)

			r = ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
			ExpectSuccess(r)

			By("Deploying endpoint on model-cache cluster")
			yamlPath := applyEndpoint(epName, clusterName)
			defer os.Remove(yamlPath)
			waitEndpointRunning(epName)
		})

		AfterAll(func() {
			deleteEndpoint(epName)
			ClusterH.EnsureDeleted(clusterName)
			RunSSH(sshUser, headIP, sshKeyFile, "rm -rf "+modelCachePath)
		})

		It("should use HostPath model cache for endpoint backend container", Label("C2613463"), func() {
			By("Checking host path model cache directory on head node")
			r := RunSSH(sshUser, headIP, sshKeyFile,
				fmt.Sprintf("stat -c '%%a %%u %%g' %s", modelCachePath))
			ExpectSuccess(r)
			Expect(strings.Fields(strings.TrimSpace(r.Stdout))).To(HaveLen(3))

			By("Checking endpoint backend_container config via Ray Serve API")
			c := getClusterFullJSON(clusterName)
			Expect(c.Status.DashboardURL).NotTo(BeEmpty())

			mcRayH := NewRayHelper(c.Status.DashboardURL)
			apps, err := mcRayH.GetServeApplications()
			Expect(err).NotTo(HaveOccurred())

			appName := fmt.Sprintf("%s_%s", profileWorkspace(), epName)
			appStatus, ok := apps.Applications[appName]
			Expect(ok).To(BeTrue(), "Ray Serve application %q should exist", appName)
			Expect(appStatus.DeployedAppConfig).NotTo(BeNil(),
				"application %q should have DeployedAppConfig", appName)
			Expect(appStatus.DeployedAppConfig.Args).NotTo(BeNil(),
				"application %q should have Args", appName)

			bc, ok := appStatus.DeployedAppConfig.Args["backend_container"]
			Expect(ok).To(BeTrue(), "application %q args should contain backend_container", appName)

			bcMap, ok := bc.(map[string]any)
			Expect(ok).To(BeTrue(), "backend_container should be a map")

			runOpts, ok := bcMap["run_options"]
			Expect(ok).To(BeTrue(), "backend_container should contain run_options")

			runOptsSlice, ok := runOpts.([]any)
			Expect(ok).To(BeTrue(), "run_options should be a list")

			found := false
			for _, opt := range runOptsSlice {
				if optStr, isStr := opt.(string); isStr && strings.Contains(optStr, modelCachePath) {
					found = true

					break
				}
			}

			Expect(found).To(BeTrue(),
				"backend_container run_options should contain host path mount with %s", modelCachePath)
		})
	})
})
