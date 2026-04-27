package e2e

import (
	"context"
	"os"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/semver"
)

// requireOldVersion returns the old version for upgrade tests, or skips.
func requireOldVersion() string {
	if profile.Cluster.OldVersion == "" {
		Skip("cluster.old_version not configured in profile, skipping upgrade tests")
	}

	return profile.Cluster.OldVersion
}

// skipIfEndpointIncompatibleOldCluster skips the endpoint-compatibility tests
// when the profile's cluster old_version is <= v1.0.0. Clusters at that version
// ship a Ray/engine base that does not support the current Engine.OldVersion
// (e.g. v0.11.2), so deploying the endpoint on the pre-upgrade cluster fails
// with an irrelevant incompatibility error. Once old_version moves past v1.0.0
// the compatibility window is valid again and the tests run.
func skipIfEndpointIncompatibleOldCluster(oldVersion string) {
	lt, err := semver.LessThan(oldVersion, "v1.0.1")
	if err != nil {
		Fail("invalid cluster.old_version " + oldVersion + ": " + err.Error())
	}

	if lt {
		Skip("cluster old_version " + oldVersion + " (<=v1.0.0) does not support the current engine old_version, skipping endpoint-compat tests")
	}
}

var _ = Describe("Cluster Upgrade", Ordered, Label("cluster", "upgrade"), func() {
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
			headIP, workerIPs, sshUser, sshPrivateKey = requireSSHProfile()
			oldVersion = requireOldVersion()
			clusterName = "e2e-ssh-upg-" + Cfg.RunID

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
			r = ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
			ExpectSuccess(r)
		})

		AfterAll(func() {
			ClusterH.EnsureDeleted(clusterName)
		})

		It("should show Upgrading then Running after version change", Label("C2642231"), func() {
			r := ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c := parseClusterJSON(r.Stdout)
			versionBefore := c.Status.Version
			Expect(versionBefore).NotTo(BeEmpty())

			newVersion := profileClusterVersion()

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

			By("Waiting for Upgrading phase")
			ClusterH.EventuallyInPhase(clusterName, v1.ClusterPhaseUpgrading, "", IntermediatePhaseTimeout)

			By("Waiting for Running phase after upgrade")
			r = ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
			ExpectSuccess(r)

			By("Verifying Status.Version == " + newVersion)
			r = ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c = parseClusterJSON(r.Stdout)
			Expect(c.Status.Version).To(Equal(newVersion),
				"cluster Status.Version should equal new version after upgrade")
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
			headIP, workerIPs, sshUser, sshPrivateKey = requireSSHProfile()
			oldVersion = requireOldVersion()
			skipIfEndpointIncompatibleOldCluster(oldVersion)

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
			r = ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
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
			code, body, err := inferChat(ep.Status.ServiceURL, "Hello before upgrade")
			Expect(err).NotTo(HaveOccurred())
			Expect(code).To(Equal(200), "inference before upgrade failed: %s", body)
			Expect(body).To(ContainSubstring("choices"))
		})

		It("should upgrade cluster and recover endpoint", Label("C2642233"), func() {
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
			r := ClusterH.Apply(yaml)
			ExpectSuccess(r)

			By("Waiting for Upgrading phase")
			ClusterH.EventuallyInPhase(clusterName, v1.ClusterPhaseUpgrading, "", IntermediatePhaseTimeout)

			By("Waiting for cluster Running after upgrade")
			r = ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
			ExpectSuccess(r)

			By("Verifying Status.Version == " + newVersion)
			r = ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c := parseClusterJSON(r.Stdout)
			Expect(c.Status.Version).To(Equal(newVersion),
				"cluster Status.Version should equal new version after upgrade")

			By("Waiting for endpoint to recover after upgrade")
			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Running"))

			code, body, err := inferChat(ep.Status.ServiceURL, "Hello after upgrade")
			Expect(err).NotTo(HaveOccurred())
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
			kubeconfig = requireK8sProfile()
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
			r = ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
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

			By("Waiting for Upgrading phase")
			ClusterH.EventuallyInPhase(clusterName, v1.ClusterPhaseUpgrading, "", IntermediatePhaseTimeout)

			By("Waiting for Running phase after upgrade")
			r = ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
			ExpectSuccess(r)

			By("Verifying Status.Version == " + newVersion)
			r = ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c = parseClusterJSON(r.Stdout)
			Expect(c.Status.Version).To(Equal(newVersion),
				"cluster Status.Version should equal new version after upgrade")
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
			kubeconfig = requireK8sProfile()
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
			r = ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
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
			ctx := context.Background()
			deploys, err := k8sH.ListDeployments(ctx, namespace, "app=inference")
			Expect(err).NotTo(HaveOccurred())

			generationBefore := map[string]int64{}
			for _, d := range deploys {
				generationBefore[d.Name] = d.Generation
			}

			Expect(generationBefore).NotTo(BeEmpty(), "should have at least one endpoint deployment")

			newVersion := profileClusterVersion()

			By("Applying cluster with new version " + newVersion)
			yaml := renderK8sClusterYAML(map[string]string{
				"name":       clusterName,
				"version":    newVersion,
				"kubeconfig": kubeconfig,
			})
			r := ClusterH.Apply(yaml)
			ExpectSuccess(r)

			By("Waiting for Upgrading phase")
			ClusterH.EventuallyInPhase(clusterName, v1.ClusterPhaseUpgrading, "", IntermediatePhaseTimeout)

			r = ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
			ExpectSuccess(r)

			By("Verifying Status.Version == " + newVersion)
			r = ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c := parseClusterJSON(r.Stdout)
			Expect(c.Status.Version).To(Equal(newVersion),
				"cluster Status.Version should equal new version after upgrade")

			waitEndpointRunning(epName)

			By("Verifying endpoint deployment generation unchanged")
			deploys, err = k8sH.ListDeployments(ctx, namespace, "app=inference")
			Expect(err).NotTo(HaveOccurred())

			for _, d := range deploys {
				if before, ok := generationBefore[d.Name]; ok {
					Expect(d.Generation).To(Equal(before),
						"endpoint deployment %s generation should not change after cluster upgrade", d.Name)
				}
			}
		})

		It("should update deployment image after endpoint config change", func() {
			ctx := context.Background()

			deploys, err := k8sH.ListDeployments(ctx, namespace, "app=inference")
			Expect(err).NotTo(HaveOccurred())

			generationBefore := map[string]int64{}
			for _, d := range deploys {
				if strings.Contains(d.Name, epName) {
					generationBefore[d.Name] = d.Generation
				}
			}

			Expect(generationBefore).NotTo(BeEmpty())

			By("Updating endpoint config with new env var")
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

			By("Verifying model-downloader initContainer image matches cluster version")
			newVersion := profileClusterVersion()

			deploys, err = k8sH.ListDeployments(ctx, namespace, "app=inference")
			Expect(err).NotTo(HaveOccurred())

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
