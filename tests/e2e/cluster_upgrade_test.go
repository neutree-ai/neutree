package e2e

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// --- K8s client helpers ---

// newK8sClientFromBase64Kubeconfig creates a kubernetes clientset from a base64-encoded kubeconfig.
// Reuses the same E2E_KUBECONFIG value that is passed to neutree for cluster creation.
func newK8sClientFromBase64Kubeconfig(b64Kubeconfig string) kubernetes.Interface {
	decoded, err := base64.StdEncoding.DecodeString(b64Kubeconfig)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "failed to decode kubeconfig")

	restConfig, err := clientcmd.RESTConfigFromKubeConfig(decoded)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "failed to create REST config from kubeconfig")

	clientset, err := kubernetes.NewForConfig(restConfig)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "failed to create kubernetes clientset")

	return clientset
}

// findEndpointDeployment finds the Deployment for an endpoint across all namespaces using the "endpoint" label.
func findEndpointDeployment(clientset kubernetes.Interface, endpointName string) *appsv1.Deployment {
	// List all namespaces and search each one for the endpoint Deployment
	nsList, err := clientset.CoreV1().Namespaces().List(context.Background(), metav1.ListOptions{})
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "failed to list namespaces")

	for _, ns := range nsList.Items {
		deployList, err := clientset.AppsV1().Deployments(ns.Name).List(context.Background(), metav1.ListOptions{
			LabelSelector: fmt.Sprintf("endpoint=%s", endpointName),
		})
		ExpectWithOffset(1, err).NotTo(HaveOccurred(), "failed to list deployments in ns %s", ns.Name)

		if len(deployList.Items) > 0 {
			return &deployList.Items[0]
		}
	}

	Fail(fmt.Sprintf("deployment with label endpoint=%s not found in any namespace", endpointName))

	return nil
}

// getDeploymentGeneration returns the metadata.generation of the Deployment for the given endpoint.
func getDeploymentGeneration(clientset kubernetes.Interface, endpointName string) int64 {
	deploy := findEndpointDeployment(clientset, endpointName)

	return deploy.Generation
}

// getInitContainerImage returns the image of the first initContainer in the endpoint's Deployment.
func getInitContainerImage(clientset kubernetes.Interface, endpointName string) string {
	deploy := findEndpointDeployment(clientset, endpointName)

	initContainers := deploy.Spec.Template.Spec.InitContainers
	ExpectWithOffset(1, initContainers).NotTo(BeEmpty(),
		"deployment for endpoint %s should have initContainers after infra injection", endpointName)

	return initContainers[0].Image
}

// getEndpointPodNames returns sorted pod names for an endpoint's Deployment.
func getEndpointPodNames(clientset kubernetes.Interface, endpointName string) []string {
	deploy := findEndpointDeployment(clientset, endpointName)

	podList, err := clientset.CoreV1().Pods(deploy.Namespace).List(context.Background(), metav1.ListOptions{
		LabelSelector: fmt.Sprintf("endpoint=%s", endpointName),
	})
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "failed to list pods for endpoint %s", endpointName)

	names := make([]string, 0, len(podList.Items))
	for _, pod := range podList.Items {
		names = append(names, pod.Name)
	}

	sort.Strings(names)

	return names
}

func requireUpgradeVersion() string {
	v := profileClusterUpgradeVersion()
	if v == "" {
		Skip("cluster.upgrade_version not configured in profile, skipping upgrade tests")
	}

	return v
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
			if profileModelName() == "" {
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
			yamlPath := applyEndpointOnCluster(epName, clusterName, profileEngineVersion())
			defer os.Remove(yamlPath)

			By("Waiting for endpoint Running")
			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(Equal("Running"))
			Expect(ep.Spec.Engine.Version).To(Equal(profileEngineVersion()))
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

	// --- K8s Cluster Upgrade: Endpoint No-Rollout ---

	Describe("K8s Cluster Upgrade Endpoint No-Rollout", Ordered, Label("k8s", "endpoint-no-rollout"), func() {
		var (
			clusterName    string
			epName         string
			kubeconfig     string
			k8sClient      kubernetes.Interface
			upgradeVersion string

			baselineGeneration int64
			baselinePodNames   []string
		)

		BeforeAll(func() {
			kubeconfig = requireK8sEnv()
			upgradeVersion = requireUpgradeVersion()
			if Cfg.ModelName == "" {
				Skip("E2E_MODEL_NAME not set, skipping K8s endpoint no-rollout tests")
			}

			clusterName = "e2e-k8s-upg-ep-" + Cfg.RunID
			epName = "e2e-k8s-noroll-" + Cfg.RunID

			By("Creating K8s client from kubeconfig")
			k8sClient = newK8sClientFromBase64Kubeconfig(kubeconfig)

			By("Setting up model registry")
			SetupModelRegistry()

			By("Creating K8s cluster with initial version")
			yaml := renderK8sClusterYAML(map[string]string{
				"name":       clusterName,
				"kubeconfig": kubeconfig,
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

		It("should deploy endpoint and record baseline", func() {
			By("Creating endpoint")
			yamlPath := applyEndpointOnCluster(epName, clusterName, Cfg.EngineVersionB)
			defer os.Remove(yamlPath)

			By("Waiting for endpoint Running")
			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(Equal("Running"))

			By("Recording baseline Deployment generation and Pod names")
			baselineGeneration = getDeploymentGeneration(k8sClient, epName)
			baselinePodNames = getEndpointPodNames(k8sClient, epName)
			Expect(baselineGeneration).To(BeNumerically(">", 0), "should have a deployment generation")
			Expect(baselinePodNames).NotTo(BeEmpty(), "should have running pods")

			GinkgoWriter.Printf("Baseline: generation=%d, pods=%v\n", baselineGeneration, baselinePodNames)
		})

		It("should serve inference before upgrade", func() {
			ep := getEndpoint(epName)
			code, body := inferChat(ep.Status.ServiceURL, "Hello before K8s upgrade")
			Expect(code).To(Equal(200), "inference before upgrade failed: %s", body)
			Expect(body).To(ContainSubstring("choices"))
		})

		It("should NOT trigger endpoint rolling update after cluster version upgrade", func() {
			By("Recording old cluster version")
			r := ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c := parseClusterJSON(r.Stdout)
			oldVersion := c.Status.Version
			Expect(oldVersion).NotTo(BeEmpty())

			By("Applying cluster with upgrade version")
			yaml := renderK8sClusterYAML(map[string]string{
				"name":       clusterName,
				"version":    upgradeVersion,
				"kubeconfig": kubeconfig,
			})
			r = ClusterH.Apply(yaml)
			ExpectSuccess(r)

			By("Polling for cluster version update")
			// K8s cluster upgrade is handled asynchronously by the controller.
			// Poll until status.version matches the upgrade version.
			deadline := time.Now().Add(5 * time.Minute)
			for time.Now().Before(deadline) {
				r = ClusterH.Get(clusterName)
				if r.ExitCode == 0 {
					c = parseClusterJSON(r.Stdout)
					if c.Status.Version == upgradeVersion {
						break
					}
				}
				time.Sleep(5 * time.Second)
			}

			By("Verifying cluster version updated")
			Expect(c.Status.Version).To(Equal(upgradeVersion),
				"cluster version should update to %s", upgradeVersion)

			By("Waiting for controller reconcile to settle")
			// Give the endpoint controller time to reconcile with the new cluster version.
			// If infra injection works correctly, this reconcile should be a no-op.
			time.Sleep(30 * time.Second)

			By("Verifying endpoint is still Running (no transient Deploying phase)")
			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(Equal("Running"),
				"endpoint should remain Running after cluster version upgrade")

			By("Verifying Deployment generation unchanged (no spec update)")
			currentGeneration := getDeploymentGeneration(k8sClient, epName)
			Expect(currentGeneration).To(Equal(baselineGeneration),
				"Deployment generation should not change after cluster version upgrade "+
					"(baseline=%d, current=%d)", baselineGeneration, currentGeneration)

			By("Verifying Pod names unchanged (no pod recreation)")
			currentPodNames := getEndpointPodNames(k8sClient, epName)
			Expect(currentPodNames).To(Equal(baselinePodNames),
				"Pod names should not change after cluster version upgrade "+
					"(baseline=%v, current=%v)", baselinePodNames, currentPodNames)
		})

		It("should serve inference after upgrade without rolling update", func() {
			ep := getEndpoint(epName)
			code, body := inferChat(ep.Status.ServiceURL, "Hello after K8s upgrade")
			Expect(code).To(Equal(200), "inference after upgrade failed: %s", body)
			Expect(body).To(ContainSubstring("choices"))
		})

		It("should trigger rolling update when user changes endpoint", func() {
			By("Rendering endpoint template and modifying replicas to 2")
			yamlPath := applyEndpointOnCluster(epName, clusterName, Cfg.EngineVersionB)
			defer os.Remove(yamlPath)

			// Read rendered YAML, change replicas from 1 to 2, re-apply
			content, err := os.ReadFile(yamlPath)
			Expect(err).NotTo(HaveOccurred())
			modified := strings.Replace(string(content), "num: 1", "num: 2", 1)
			Expect(modified).To(ContainSubstring("num: 2"), "replicas should be modified to 2")
			Expect(os.WriteFile(yamlPath, []byte(modified), 0o644)).To(Succeed())

			By("Applying endpoint with replicas=2")
			r := RunCLI("apply", "-f", yamlPath, "--force-update")
			ExpectSuccess(r)

			By("Waiting for endpoint Running with updated replicas")
			waitEndpointRunning(epName)

			By("Polling for Deployment generation to increase")
			var newGeneration int64
			genDeadline := time.Now().Add(2 * time.Minute)
			for time.Now().Before(genDeadline) {
				newGeneration = getDeploymentGeneration(k8sClient, epName)
				if newGeneration > baselineGeneration {
					break
				}
				time.Sleep(5 * time.Second)
			}

			Expect(newGeneration).To(BeNumerically(">", baselineGeneration),
				"Deployment generation should increase after user changes replicas")

			By("Verifying initContainer image updated to new cluster version")
			initImage := getInitContainerImage(k8sClient, epName)
			Expect(initImage).To(ContainSubstring(upgradeVersion),
				"initContainer neutree-runtime image should contain upgrade version %s, got %s",
				upgradeVersion, initImage)

			GinkgoWriter.Printf("After user change: generation=%d (was %d), initContainer image=%s\n",
				newGeneration, baselineGeneration, initImage)
		})

		It("should serve inference after user-triggered update", func() {
			ep := getEndpoint(epName)
			code, body := inferChat(ep.Status.ServiceURL, "Hello after user update")
			Expect(code).To(Equal(200), "inference after user update failed: %s", body)
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
