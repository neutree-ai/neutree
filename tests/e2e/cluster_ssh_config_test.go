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

			r := RunSSH(sshUser, headIP, sshKeyFile,
				fmt.Sprintf("stat -c '%%a %%u %%g' %s 2>/dev/null || echo 'NOT_FOUND'", cachePath))
			ExpectSuccess(r)
			Expect(r.Stdout).NotTo(ContainSubstring("NOT_FOUND"),
				"model cache directory %s should exist", cachePath)

			fields := strings.Fields(strings.TrimSpace(r.Stdout))
			Expect(len(fields)).To(BeNumerically(">=", 1),
				"stat output should contain permission mode, got: %s", r.Stdout)
			mode := fields[0]
			Expect(mode).NotTo(Equal("000"),
				"model cache directory should have non-zero permissions, got %s", mode)
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

			found := false
			for _, appStatus := range apps.Applications {
				if appStatus.DeployedAppConfig == nil || appStatus.DeployedAppConfig.Args == nil {
					continue
				}

				bc, ok := appStatus.DeployedAppConfig.Args["backend_container"]
				if !ok {
					continue
				}

				bcMap, isMap := bc.(map[string]interface{})
				if !isMap {
					continue
				}

				runOpts, ok := bcMap["run_options"]
				if !ok {
					continue
				}

				runOptsSlice, isList := runOpts.([]interface{})
				if !isList {
					continue
				}

				for _, opt := range runOptsSlice {
					optStr, isStr := opt.(string)
					if isStr && strings.Contains(optStr, profile.ModelCache.HostPath) {
						found = true

						break
					}
				}

				if found {
					break
				}
			}

			Expect(found).To(BeTrue(),
				"backend_container run_options should contain host path mount with %s", profile.ModelCache.HostPath)
		})
	})
})
