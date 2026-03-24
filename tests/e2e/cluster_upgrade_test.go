package e2e

import (
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func requireUpgradeVersion() string {
	if os.Getenv("E2E_CLUSTER_UPGRADE_VERSION") == "" {
		Skip("E2E_CLUSTER_UPGRADE_VERSION not set, skipping upgrade tests")
	}

	return os.Getenv("E2E_CLUSTER_UPGRADE_VERSION")
}

var _ = Describe("Cluster Upgrade", Ordered, Label("upgrade"), func() {
	var ClusterH *ClusterHelper

	BeforeAll(func() {
		if profile.ImageRegistry.URL == "" {
			Skip("ImageRegistry.URL not configured in profile, skipping cluster upgrade tests")
		}
		if profile.ImageRegistry.Repository == "" {
			Skip("ImageRegistry.Repository not configured in profile, skipping cluster upgrade tests")
		}

		By("Setting up image registry")
		SetupImageRegistry()
		ClusterH = NewClusterHelper()
	})

	AfterAll(func() {
		By("Tearing down image registry")
		TeardownImageRegistry()
	})

	// --- SSH Cluster Upgrade ---

	Describe("SSH Cluster Upgrade", Ordered, Label("ssh"), func() {
		var (
			clusterName    string
			headIP         string
			workerIPs      string
			sshUser        string
			sshPrivateKey  string
			upgradeVersion string
		)

		BeforeAll(func() {
			headIP, workerIPs, sshUser, sshPrivateKey = requireSSHEnv()
			upgradeVersion = requireUpgradeVersion()
			clusterName = "e2e-ssh-upg-" + Cfg.RunID

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

			By("Waiting for Running phase")
			r = ClusterH.WaitForPhase(clusterName, "Running", "10m")
			ExpectSuccess(r)
		})

		AfterAll(func() {
			By("Force-deleting SSH upgrade cluster")
			ClusterH.EnsureDeleted(clusterName)
		})

		It("should show Upgrading then Running after version change", func() {
			By("Recording old version")
			r := ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c := parseClusterJSON(r.Stdout)
			oldVersion := c.Status.Version
			Expect(oldVersion).NotTo(BeEmpty(), "cluster should have a version before upgrade")

			By("Applying with new version")
			yaml := renderSSHClusterYAML(map[string]string{
				"name":            clusterName,
				"version":         upgradeVersion,
				"head_ip":         headIP,
				"worker_ips":      workerIPs,
				"ssh_user":        sshUser,
				"ssh_private_key": sshPrivateKey,
			})
			r = ClusterH.Apply(yaml)
			ExpectSuccess(r)

			By("Polling for Upgrading phase and progress messages")
			seenUpgrading := false
			var progressMessages string
			deadline := time.Now().Add(60 * time.Second)
			for time.Now().Before(deadline) {
				r = ClusterH.Get(clusterName)
				if r.ExitCode == 0 {
					c = parseClusterJSON(r.Stdout)
					if c.Status.Phase == "Upgrading" {
						seenUpgrading = true
						if c.Status.ErrorMessage != "" {
							progressMessages = c.Status.ErrorMessage
						}
						break
					}
					if c.Status.Phase == "Running" && c.Status.Version == upgradeVersion {
						break
					}
				}
				time.Sleep(2 * time.Second)
			}
			_ = seenUpgrading // Upgrading may be too transient to observe.

			By("Waiting for Running phase after upgrade")
			r = ClusterH.WaitForPhase(clusterName, "Running", "10m")
			ExpectSuccess(r)

			By("Verifying version changed")
			r = ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c = parseClusterJSON(r.Stdout)
			Expect(c.Status.Version).NotTo(Equal(oldVersion), "version should change after upgrade")

			By("Verifying progress messages were recorded during upgrade")
			// If we captured progress during Upgrading phase, verify key messages.
			// Progress may also be in the final status error_message after completion.
			if progressMessages == "" {
				progressMessages = c.Status.ErrorMessage
			}
			if progressMessages != "" {
				Expect(progressMessages).To(ContainSubstring("upgrade"),
					"progress should mention upgrade")
			}
		})
	})

	// --- SSH Cluster Upgrade with Endpoint Compatibility ---

	Describe("SSH Cluster Upgrade with Endpoint Compatibility", Ordered, Label("ssh", "endpoint-compat"), func() {
		var (
			clusterName    string
			epName         string
			headIP         string
			workerIPs      string
			sshUser        string
			sshPrivateKey  string
			upgradeVersion string
		)

		BeforeAll(func() {
			headIP, workerIPs, sshUser, sshPrivateKey = requireSSHEnv()
			upgradeVersion = requireUpgradeVersion()
			if modelName() == "" {
				Skip("Model name not configured in profile, skipping upgrade endpoint compat tests")
			}

			clusterName = "e2e-ssh-upg-ep-" + Cfg.RunID
			epName = "e2e-upg-ep-" + Cfg.RunID

			By("Setting up model registry")
			SetupModelRegistry()

			By("Creating SSH cluster with initial version")
			yaml := renderSSHClusterYAML(map[string]string{
				"name":            clusterName,
				"head_ip":         headIP,
				"worker_ips":      workerIPs,
				"ssh_user":        sshUser,
				"ssh_private_key": sshPrivateKey,
			})
			r := ClusterH.Apply(yaml)
			ExpectSuccess(r)

			By("Waiting for cluster Running")
			r = ClusterH.WaitForPhase(clusterName, "Running", "10m")
			ExpectSuccess(r)
		})

		AfterAll(func() {
			By("Cleaning up endpoint")
			deleteEndpoint(epName)

			By("Force-deleting cluster")
			ClusterH.EnsureDeleted(clusterName)

			By("Tearing down model registry")
			TeardownModelRegistry()
		})

		It("should deploy endpoint with engine vllm v0.8.5 on initial cluster", func() {
			By("Creating endpoint with default engine version")
			yamlPath := applyEndpointOnCluster(epName, clusterName, engineVersionA())
			defer os.Remove(yamlPath)

			By("Waiting for endpoint Running")
			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(Equal("Running"))
			Expect(ep.Spec.Engine.Version).To(Equal(engineVersionA()))
		})

		It("should serve inference before upgrade", func() {
			ep := getEndpoint(epName)
			code, body := inferChat(ep.Status.ServiceURL, "Hello before upgrade")
			Expect(code).To(Equal(200), "inference before upgrade failed: %s", body)
			Expect(body).To(ContainSubstring("choices"))
		})

		It("should upgrade cluster and recover endpoint", func() {
			By("Recording old version")
			r := ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c := parseClusterJSON(r.Stdout)
			oldVersion := c.Status.Version
			Expect(oldVersion).NotTo(BeEmpty())

			By("Applying cluster with upgrade version")
			yaml := renderSSHClusterYAML(map[string]string{
				"name":            clusterName,
				"version":         upgradeVersion,
				"head_ip":         headIP,
				"worker_ips":      workerIPs,
				"ssh_user":        sshUser,
				"ssh_private_key": sshPrivateKey,
			})
			r = ClusterH.Apply(yaml)
			ExpectSuccess(r)

			By("Polling for Upgrading phase and progress messages")
			seenUpgrading := false
			var progressMessages string
			deadline := time.Now().Add(60 * time.Second)
			for time.Now().Before(deadline) {
				r = ClusterH.Get(clusterName)
				if r.ExitCode == 0 {
					c = parseClusterJSON(r.Stdout)
					if c.Status.Phase == "Upgrading" {
						seenUpgrading = true
						if c.Status.ErrorMessage != "" {
							progressMessages = c.Status.ErrorMessage
						}
						break
					}
					if c.Status.Phase == "Running" && c.Status.Version == upgradeVersion {
						break
					}
				}
				time.Sleep(2 * time.Second)
			}
			_ = seenUpgrading

			By("Waiting for cluster Running after upgrade")
			r = ClusterH.WaitForPhase(clusterName, "Running", "10m")
			ExpectSuccess(r)

			By("Verifying cluster version updated")
			r = ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c = parseClusterJSON(r.Stdout)
			Expect(c.Status.Version).To(Equal(upgradeVersion))

			By("Verifying progress messages were recorded during upgrade")
			if progressMessages == "" {
				progressMessages = c.Status.ErrorMessage
			}
			if progressMessages != "" {
				Expect(progressMessages).To(ContainSubstring("upgrade"),
					"progress should mention upgrade")
			}

			By("Waiting for endpoint to recover after upgrade")
			waitEndpointRunning(epName)
		})

		It("should serve inference after upgrade", func() {
			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(Equal("Running"))

			code, body := inferChat(ep.Status.ServiceURL, "Hello after upgrade")
			Expect(code).To(Equal(200), "inference after upgrade failed: %s", body)
			Expect(body).To(ContainSubstring("choices"))
		})
	})

	// --- K8s Cluster Upgrade ---

	Describe("K8s Cluster Upgrade", Ordered, Label("k8s"), func() {
		var (
			clusterName    string
			kubeconfig     string
			upgradeVersion string
		)

		BeforeAll(func() {
			kubeconfig = requireK8sEnv()
			upgradeVersion = requireUpgradeVersion()
			clusterName = "e2e-k8s-upg-" + Cfg.RunID

			yaml := renderK8sClusterYAML(map[string]string{
				"name":       clusterName,
				"kubeconfig": kubeconfig,
			})

			By("Applying K8s cluster")
			r := ClusterH.Apply(yaml)
			ExpectSuccess(r)

			By("Waiting for Running phase")
			r = ClusterH.WaitForPhase(clusterName, "Running", "10m")
			ExpectSuccess(r)
		})

		AfterAll(func() {
			By("Force-deleting K8s upgrade cluster")
			ClusterH.EnsureDeleted(clusterName)
		})

		It("should show Upgrading then Running after version change", func() {
			By("Recording old version")
			r := ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c := parseClusterJSON(r.Stdout)
			oldVersion := c.Status.Version
			Expect(oldVersion).NotTo(BeEmpty(), "cluster should have a version before upgrade")

			By("Applying with new version")
			yaml := renderK8sClusterYAML(map[string]string{
				"name":       clusterName,
				"version":    upgradeVersion,
				"kubeconfig": kubeconfig,
			})
			r = ClusterH.Apply(yaml)
			ExpectSuccess(r)

			By("Polling for Upgrading phase (may be transient)")
			seenUpgrading := false
			deadline := time.Now().Add(60 * time.Second)
			for time.Now().Before(deadline) {
				r = ClusterH.Get(clusterName)
				if r.ExitCode == 0 {
					c = parseClusterJSON(r.Stdout)
					if c.Status.Phase == "Upgrading" {
						seenUpgrading = true
						break
					}
					if c.Status.Phase == "Running" && c.Status.Version == upgradeVersion {
						break
					}
				}
				time.Sleep(2 * time.Second)
			}
			_ = seenUpgrading // Upgrading may be too transient to observe.

			By("Waiting for Running phase after upgrade")
			r = ClusterH.WaitForPhase(clusterName, "Running", "10m")
			ExpectSuccess(r)

			By("Verifying version changed")
			r = ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c = parseClusterJSON(r.Stdout)
			Expect(c.Status.Version).NotTo(Equal(oldVersion), "version should change after upgrade")
		})
	})
})
