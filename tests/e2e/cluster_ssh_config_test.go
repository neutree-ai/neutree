package e2e

import (
	"fmt"
	"os"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SSH Cluster Config", Ordered, Label("cluster", "ssh", "config"), func() {
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

	// --- Create Verification ---

	Describe("Create Verification", Ordered, func() {
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
			clusterName = "e2e-ssh-verify-" + Cfg.RunID

			overrides := map[string]string{
				"name":            clusterName,
				"head_ip":         headIP,
				"worker_ips":      workerIPs,
				"ssh_user":        sshUser,
				"ssh_private_key": sshPrivateKey,
			}

			if profile.ModelCache.HostPath != "" {
				overrides["model_caches_yaml"] = fmt.Sprintf("    model_caches:\n      - name: hp-cache\n        host_path:\n          path: \"%s\"\n", profile.ModelCache.HostPath)
			}

			yaml := renderSSHClusterYAML(overrides)

			r := ClusterH.Apply(yaml)
			ExpectSuccess(r)

			r = ClusterH.WaitForPhase(clusterName, "Running", "10m")
			ExpectSuccess(r)
		})

		AfterAll(func() {
			ClusterH.EnsureDeleted(clusterName)
		})

		It("should have ray_container running on all nodes", Label("C2613077"), func() {
			r := RunSSH(sshUser, headIP, sshKeyFile,
				"docker ps --filter name=ray_container --format '{{.Names}}'")
			ExpectSuccess(r)
			Expect(r.Stdout).To(ContainSubstring("ray_container"))

			if workerIPs != "" {
				for _, ip := range strings.Split(workerIPs, ",") {
					ip = strings.TrimSpace(ip)
					if ip == "" {
						continue
					}

					r = RunSSH(sshUser, ip, sshKeyFile,
						"docker ps --filter name=ray_container --format '{{.Names}}'")
					ExpectSuccess(r)
					Expect(r.Stdout).To(ContainSubstring("ray_container"),
						"ray_container should be running on worker %s", ip)
				}
			}
		})

		It("should have correct HostPath permissions on all nodes", Label("C2613078"), func() {
			cachePath := profile.ModelCache.HostPath
			if cachePath == "" {
				cachePath = "/opt/neutree/model-cache"
			}

			nodes := []string{headIP}
			for _, ip := range strings.Split(workerIPs, ",") {
				ip = strings.TrimSpace(ip)
				if ip != "" {
					nodes = append(nodes, ip)
				}
			}

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

			r = ClusterH.WaitForDelete(clusterName, "10m")
			ExpectSuccess(r)

			r = RunSSH(sshUser, headIP, sshKeyFile,
				"docker ps -a --filter name=ray_container --format '{{.Names}}'")
			ExpectSuccess(r)
			Expect(r.Stdout).NotTo(ContainSubstring("ray_container"))

			if workerIPs != "" {
				for _, ip := range strings.Split(workerIPs, ",") {
					ip = strings.TrimSpace(ip)
					if ip == "" {
						continue
					}

					r = RunSSH(sshUser, ip, sshKeyFile,
						"docker ps -a --filter name=ray_container --format '{{.Names}}'")
					ExpectSuccess(r)
					Expect(r.Stdout).NotTo(ContainSubstring("ray_container"),
						"ray_container should be cleaned up from worker %s", ip)
				}
			}
		})

		It("should preserve model cache after deletion", Label("C2613100"), func() {
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
			workerIPs     string
			sshUser       string
			sshPrivateKey string
		)

		BeforeAll(func() {
			headIP, workerIPs, sshUser, sshPrivateKey = requireSSHEnv()
			if workerIPs == "" {
				Skip("No worker IPs configured, skipping worker edit tests")
			}

			clusterName = "e2e-ssh-edit-" + Cfg.RunID

			yaml := renderSSHClusterYAML(map[string]string{
				"name":            clusterName,
				"head_ip":         headIP,
				"ssh_user":        sshUser,
				"ssh_private_key": sshPrivateKey,
			})

			r := ClusterH.Apply(yaml)
			ExpectSuccess(r)

			r = ClusterH.WaitForPhase(clusterName, "Running", "10m")
			ExpectSuccess(r)
		})

		AfterAll(func() {
			ClusterH.EnsureDeleted(clusterName)
		})

		It("should add worker node and reach Running", Label("C2612830"), func() {
			r := ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c := parseClusterJSON(r.Stdout)
			oldNodes := c.Status.DesiredNodes
			oldHash := c.Status.ObservedSpecHash

			yaml := renderSSHClusterYAML(map[string]string{
				"name":            clusterName,
				"head_ip":         headIP,
				"worker_ips":      workerIPs,
				"ssh_user":        sshUser,
				"ssh_private_key": sshPrivateKey,
			})
			r = ClusterH.Apply(yaml)
			ExpectSuccess(r)

			ClusterH.WaitForSpecChange(clusterName, oldHash, 60*time.Second)

			r = ClusterH.WaitForPhase(clusterName, "Running", "10m")
			ExpectSuccess(r)

			r = ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c = parseClusterJSON(r.Stdout)
			Expect(c.Status.DesiredNodes).To(BeNumerically(">", oldNodes))
			Expect(c.Status.ReadyNodes).To(Equal(c.Status.DesiredNodes))
		})

		It("should remove worker node and reach Running", Label("C2612831"), func() {
			r := ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c := parseClusterJSON(r.Stdout)
			oldNodes := c.Status.DesiredNodes
			oldHash := c.Status.ObservedSpecHash

			yaml := renderSSHClusterYAML(map[string]string{
				"name":            clusterName,
				"head_ip":         headIP,
				"ssh_user":        sshUser,
				"ssh_private_key": sshPrivateKey,
			})
			r = ClusterH.Apply(yaml)
			ExpectSuccess(r)

			ClusterH.WaitForSpecChange(clusterName, oldHash, 60*time.Second)

			r = ClusterH.WaitForPhase(clusterName, "Running", "10m")
			ExpectSuccess(r)

			r = ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c = parseClusterJSON(r.Stdout)
			Expect(c.Status.DesiredNodes).To(BeNumerically("<", oldNodes))
			Expect(c.Status.ReadyNodes).To(Equal(c.Status.DesiredNodes),
				"ReadyNodes should match DesiredNodes after worker removal")
		})
	})

	// --- Model Cache Edit ---

	Describe("Model Cache Edit", Ordered, Label("edit"), func() {
		var (
			clusterName   string
			epName        string
			headIP        string
			workerIPs     string
			sshUser       string
			sshPrivateKey string
			sshKeyFile    string
		)

		BeforeAll(func() {
			if profile.ModelCache.HostPath == "" {
				Skip("ModelCache.HostPath not configured in profile")
			}

			headIP, workerIPs, sshUser, sshPrivateKey = requireSSHEnv()
			if len(profile.SSHNodes) == 0 || profile.SSHNodes[0].KeyFile == "" {
				Skip("SSH key file path not configured in profile")
			}

			sshKeyFile = expandHome(profile.SSHNodes[0].KeyFile)
			clusterName = "e2e-ssh-mc-" + Cfg.RunID
			epName = "e2e-ssh-mc-ep-" + Cfg.RunID

			By("Setting up model registry")
			SetupModelRegistry()

			hostPathYAML := fmt.Sprintf("    model_caches:\n      - name: test-cache\n        host_path:\n          path: \"%s\"\n",
				profile.ModelCache.HostPath)

			yaml := renderSSHClusterYAML(map[string]string{
				"name":              clusterName,
				"head_ip":           headIP,
				"worker_ips":        workerIPs,
				"ssh_user":          sshUser,
				"ssh_private_key":   sshPrivateKey,
				"model_caches_yaml": hostPathYAML,
			})

			r := ClusterH.Apply(yaml)
			ExpectSuccess(r)

			r = ClusterH.WaitForPhase(clusterName, "Running", "10m")
			ExpectSuccess(r)

			By("Deploying endpoint on model-cache cluster")
			yamlPath := applyEndpointOnCluster(epName, clusterName, profileEngineVersion())
			defer os.Remove(yamlPath)
			waitEndpointRunning(epName)
		})

		AfterAll(func() {
			deleteEndpoint(epName)
			ClusterH.EnsureDeleted(clusterName)
		})

		It("should have HostPath model cache mounted in ray_container", Label("C2613463"), func() {
			By("Checking ray_container source mounts on head node")
			r := RunSSH(sshUser, headIP, sshKeyFile,
				"docker inspect ray_container --format '{{range .Mounts}}{{.Source}} {{end}}'")
			ExpectSuccess(r)
			Expect(r.Stdout).To(ContainSubstring(profile.ModelCache.HostPath),
				"host path %s should be mounted as source in ray_container", profile.ModelCache.HostPath)

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
				if optStr, isStr := opt.(string); isStr && strings.Contains(optStr, profile.ModelCache.HostPath) {
					found = true

					break
				}
			}

			Expect(found).To(BeTrue(),
				"backend_container run_options should contain host path mount with %s", profile.ModelCache.HostPath)
		})
	})
})
