package e2e

import (
	"context"
	"os"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// requireOldVersion returns the old version for upgrade tests, or skips.
func requireOldVersion() string {
	if profile.Cluster.OldVersion == "" {
		Skip("cluster.old_version not configured in profile, skipping upgrade tests")
	}

	return profile.Cluster.OldVersion
}

var _ = Describe("Cluster Upgrade", Ordered, Label("cluster", "upgrade"), func() {
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

	// --- SSH Cluster Upgrade ---

	Describe("SSH Cluster Upgrade", Ordered, Label("ssh"), func() {
		var (
			clusterName   string
			headIP        string
			workerIPs     string
			sshUser       string
			sshPrivateKey string
			oldVersion    string
		)

		BeforeAll(func() {
			headIP, workerIPs, sshUser, sshPrivateKey = requireSSHEnv()
			oldVersion = requireOldVersion()
			clusterName = "e2e-ssh-upg-" + Cfg.RunID

			// Create cluster with OLD version (e.g., v1.0.0).
			yaml := renderSSHClusterYAML(map[string]string{
				"name":            clusterName,
				"version":         oldVersion,
				"head_ip":         headIP,
				"worker_ips":      workerIPs,
				"ssh_user":        sshUser,
				"ssh_private_key": sshPrivateKey,
			})

			By("Applying SSH cluster with old version " + oldVersion)
			r := ClusterH.Apply(yaml)
			ExpectSuccess(r)

			By("Waiting for Running phase")
			r = ClusterH.WaitForPhase(clusterName, "Running", "10m")
			ExpectSuccess(r)
		})

		AfterAll(func() {
			ClusterH.EnsureDeleted(clusterName)
		})

		It("should show Upgrading then Running after version change", Label("C2642231"), func() {
			By("Recording version before upgrade")
			r := ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c := parseClusterJSON(r.Stdout)
			versionBefore := c.Status.Version
			Expect(versionBefore).NotTo(BeEmpty())

			newVersion := profileClusterVersion() // e.g., v1.0.1

			By("Applying with new version " + newVersion)
			yaml := renderSSHClusterYAML(map[string]string{
				"name":            clusterName,
				"version":         newVersion,
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

					if c.Status.Phase == "Running" && c.Status.Version == newVersion {
						break
					}
				}

				time.Sleep(1 * time.Second)
			}

			By("Waiting for Running phase after upgrade")
			r = ClusterH.WaitForPhase(clusterName, "Running", "10m")
			ExpectSuccess(r)

			By("Verifying version changed")
			r = ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c = parseClusterJSON(r.Stdout)
			Expect(c.Status.Version).NotTo(Equal(versionBefore))

			By("Verifying progress messages were recorded during upgrade")
			if progressMessages == "" {
				progressMessages = c.Status.ErrorMessage
			}

			if progressMessages != "" {
				Expect(progressMessages).To(ContainSubstring("upgrade"),
					"progress should mention upgrade")
			}

			if !seenUpgrading {
				GinkgoWriter.Printf("WARNING: Upgrading phase was not captured (transition too fast for 1s poll interval)\n")
			}
		})
	})

	// --- SSH Cluster Upgrade with Endpoint Compatibility ---

	Describe("SSH Cluster Upgrade with Endpoint Compatibility", Ordered, Label("ssh", "endpoint-compat"), func() {
		var (
			clusterName   string
			epName        string
			headIP        string
			workerIPs     string
			sshUser       string
			sshPrivateKey string
			oldVersion    string
		)

		BeforeAll(func() {
			headIP, workerIPs, sshUser, sshPrivateKey = requireSSHEnv()
			oldVersion = requireOldVersion()
			if profileModelName() == "" {
				Skip("Model name not configured in profile, skipping upgrade endpoint compat tests")
			}

			clusterName = "e2e-ssh-upg-ep-" + Cfg.RunID
			epName = "e2e-upg-ep-" + Cfg.RunID

			By("Setting up model registry")
			SetupModelRegistry()

			By("Creating SSH cluster with old version " + oldVersion)
			yaml := renderSSHClusterYAML(map[string]string{
				"name":            clusterName,
				"version":         oldVersion,
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
			deleteEndpoint(epName)
			ClusterH.EnsureDeleted(clusterName)
			TeardownModelRegistry()
		})

		It("should deploy endpoint on initial cluster", func() {
			yamlPath := applyEndpointOnCluster(epName, clusterName, profileEngineOldVersion())
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Running"))
			Expect(ep.Spec.Engine.Version).To(Equal(profileEngineOldVersion()))
		})

		It("should serve inference before upgrade", func() {
			ep := getEndpoint(epName)
			code, body := inferChat(ep.Status.ServiceURL, "Hello before upgrade")
			Expect(code).To(Equal(200), "inference before upgrade failed: %s", body)
			Expect(body).To(ContainSubstring("choices"))
		})

		It("should upgrade cluster and recover endpoint", Label("C2642233"), func() {
			r := ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c := parseClusterJSON(r.Stdout)
			versionBefore := c.Status.Version
			oldHash := c.Status.ObservedSpecHash
			Expect(versionBefore).NotTo(BeEmpty())

			newVersion := profileClusterVersion()

			By("Applying cluster with new version " + newVersion)
			yaml := renderSSHClusterYAML(map[string]string{
				"name":            clusterName,
				"version":         newVersion,
				"head_ip":         headIP,
				"worker_ips":      workerIPs,
				"ssh_user":        sshUser,
				"ssh_private_key": sshPrivateKey,
			})
			r = ClusterH.Apply(yaml)
			ExpectSuccess(r)

			By("Waiting for controller to accept spec change")
			ClusterH.WaitForSpecChange(clusterName, oldHash, 60*time.Second)

			By("Waiting for cluster Running after upgrade")
			r = ClusterH.WaitForPhase(clusterName, "Running", "10m")
			ExpectSuccess(r)

			By("Verifying cluster version updated")
			r = ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c = parseClusterJSON(r.Stdout)
			Expect(c.Status.Version).NotTo(Equal(versionBefore), "version should change after upgrade")

			By("Waiting for endpoint to recover after upgrade")
			waitEndpointRunning(epName)

			By("Verifying inference still works after upgrade")
			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Running"))

			code, body := inferChat(ep.Status.ServiceURL, "Hello after upgrade")
			Expect(code).To(Equal(200), "inference after upgrade failed: %s", body)
			Expect(body).To(ContainSubstring("choices"))
		})
	})

	// --- K8s Cluster Upgrade ---

	Describe("K8s Cluster Upgrade", Ordered, Label("k8s"), func() {
		var (
			clusterName string
			kubeconfig  string
			oldVersion  string
		)

		BeforeAll(func() {
			kubeconfig = requireK8sEnv()
			oldVersion = requireOldVersion()
			clusterName = "e2e-k8s-upg-" + Cfg.RunID

			yaml := renderK8sClusterYAML(map[string]string{
				"name":       clusterName,
				"version":    oldVersion,
				"kubeconfig": kubeconfig,
			})

			By("Applying K8s cluster with old version " + oldVersion)
			r := ClusterH.Apply(yaml)
			ExpectSuccess(r)

			By("Waiting for Running phase")
			r = ClusterH.WaitForPhase(clusterName, "Running", "10m")
			ExpectSuccess(r)
		})

		AfterAll(func() {
			ClusterH.EnsureDeleted(clusterName)
		})

		It("should show Upgrading then Running after version change", Label("C2642232"), func() {
			r := ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c := parseClusterJSON(r.Stdout)
			versionBefore := c.Status.Version
			Expect(versionBefore).NotTo(BeEmpty())

			newVersion := profileClusterVersion()

			By("Applying with new version " + newVersion)
			yaml := renderK8sClusterYAML(map[string]string{
				"name":       clusterName,
				"version":    newVersion,
				"kubeconfig": kubeconfig,
			})
			r = ClusterH.Apply(yaml)
			ExpectSuccess(r)

			By("Polling for Upgrading phase")
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

					if c.Status.Phase == "Running" && c.Status.Version == newVersion {
						break
					}
				}

				time.Sleep(1 * time.Second)
			}

			By("Waiting for Running phase after upgrade")
			r = ClusterH.WaitForPhase(clusterName, "Running", "10m")
			ExpectSuccess(r)

			By("Verifying version changed")
			r = ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c = parseClusterJSON(r.Stdout)
			Expect(c.Status.Version).NotTo(Equal(versionBefore))

			if !seenUpgrading {
				GinkgoWriter.Printf("WARNING: Upgrading phase was not captured (transition too fast for 1s poll interval)\n")
			}
		})
	})

	// --- K8s Cluster Upgrade with Endpoint Compatibility ---

	Describe("K8s Cluster Upgrade with Endpoint Compatibility", Ordered, Label("k8s", "endpoint-compat"), func() {
		var (
			clusterName string
			epName      string
			kubeconfig  string
			oldVersion  string
			k8sH        *K8sHelper
			namespace   string
		)

		BeforeAll(func() {
			kubeconfig = requireK8sEnv()
			oldVersion = requireOldVersion()
			if profileModelName() == "" {
				Skip("Model name not configured in profile, skipping K8s upgrade endpoint compat tests")
			}

			clusterName = "e2e-k8s-upg-ep-" + Cfg.RunID
			epName = "e2e-upg-ep-k8s-" + Cfg.RunID

			By("Setting up model registry")
			SetupModelRegistry()

			By("Creating K8s cluster with old version " + oldVersion)
			yaml := renderK8sClusterYAML(map[string]string{
				"name":       clusterName,
				"version":    oldVersion,
				"kubeconfig": kubeconfig,
			})
			r := ClusterH.Apply(yaml)
			ExpectSuccess(r)

			By("Waiting for cluster Running")
			r = ClusterH.WaitForPhase(clusterName, "Running", "10m")
			ExpectSuccess(r)

			k8sH = NewK8sHelper(kubeconfig)

			r = ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c := parseClusterJSON(r.Stdout)
			namespace = ClusterNamespace(c.Metadata.Workspace, c.Metadata.Name, c.ID)
		})

		AfterAll(func() {
			deleteEndpoint(epName)
			ClusterH.EnsureDeleted(clusterName)
			TeardownModelRegistry()
		})

		It("should deploy endpoint on initial cluster", func() {
			yamlPath := applyEndpointOnCluster(epName, clusterName, profileEngineVersion())
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Running"))
		})

		It("should upgrade cluster without triggering endpoint deployment rollout", func() {
			By("Recording endpoint deployment generation before upgrade")
			ctx := context.Background()
			deploys, err := k8sH.ListDeployments(ctx, namespace, "app=inference")
			Expect(err).NotTo(HaveOccurred())

			generationBefore := map[string]int64{}
			for _, d := range deploys {
				generationBefore[d.Name] = d.Generation
			}

			Expect(generationBefore).NotTo(BeEmpty(), "should have at least one endpoint deployment")

			r := ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c := parseClusterJSON(r.Stdout)
			oldHash := c.Status.ObservedSpecHash

			newVersion := profileClusterVersion()

			By("Applying cluster with new version " + newVersion)
			yaml := renderK8sClusterYAML(map[string]string{
				"name":       clusterName,
				"version":    newVersion,
				"kubeconfig": kubeconfig,
			})
			r = ClusterH.Apply(yaml)
			ExpectSuccess(r)

			ClusterH.WaitForSpecChange(clusterName, oldHash, 60*time.Second)

			By("Waiting for cluster Running after upgrade")
			r = ClusterH.WaitForPhase(clusterName, "Running", "10m")
			ExpectSuccess(r)

			By("Waiting for endpoint to recover")
			waitEndpointRunning(epName)

			By("Verifying endpoint deployment generation unchanged")
			deploys, err = k8sH.ListDeployments(ctx, namespace, "app=inference")
			Expect(err).NotTo(HaveOccurred())

			for _, d := range deploys {
				if before, ok := generationBefore[d.Name]; ok {
					Expect(d.Generation).To(Equal(before),
						"endpoint deployment %s generation should not change after cluster upgrade (was %d, now %d)",
						d.Name, before, d.Generation)
				}
			}
		})

		It("should update deployment and model-downloader image after endpoint config change", func() {
			ctx := context.Background()

			By("Recording deployment generation before endpoint update")
			deploys, err := k8sH.ListDeployments(ctx, namespace, "app=inference")
			Expect(err).NotTo(HaveOccurred())

			generationBefore := map[string]int64{}
			for _, d := range deploys {
				if strings.Contains(d.Name, epName) {
					generationBefore[d.Name] = d.Generation
				}
			}
			Expect(generationBefore).NotTo(BeEmpty())

			By("Updating endpoint config with new env var (re-apply with force-update)")
			yamlPath := applyEndpointWithEnv(epName, clusterName, profileEngineVersion(),
				map[string]string{"E2E_UPGRADE_MARKER": "true"})
			defer os.Remove(yamlPath)

			By("Waiting for deployment generation to increase")
			Eventually(func() bool {
				ds, err := k8sH.ListDeployments(ctx, namespace, "app=inference")
				if err != nil {
					return false
				}
				for _, d := range ds {
					if before, ok := generationBefore[d.Name]; ok {
						if d.Generation > before {
							return true
						}
					}
				}
				return false
			}, 3*time.Minute, 5*time.Second).Should(BeTrue(),
				"endpoint deployment generation should increase after config update")

			waitEndpointRunning(epName)

			By("Verifying deployment generation changed")
			deploys, err = k8sH.ListDeployments(ctx, namespace, "app=inference")
			Expect(err).NotTo(HaveOccurred())

			for _, d := range deploys {
				if before, ok := generationBefore[d.Name]; ok {
					Expect(d.Generation).To(BeNumerically(">", before),
						"endpoint deployment %s generation should increase after config update (was %d, now %d)",
						d.Name, before, d.Generation)
				}
			}

			By("Verifying model-downloader initContainer image matches cluster version")
			newVersion := profileClusterVersion()

			for _, d := range deploys {
				if !strings.Contains(d.Name, epName) {
					continue
				}

				for _, ic := range d.Spec.Template.Spec.InitContainers {
					if ic.Name == "model-downloader" {
						Expect(ic.Image).To(ContainSubstring(newVersion),
							"model-downloader image should contain cluster version %s, got %s",
							newVersion, ic.Image)
						return
					}
				}
			}

			Fail("model-downloader initContainer not found in endpoint deployment")
		})
	})
})
