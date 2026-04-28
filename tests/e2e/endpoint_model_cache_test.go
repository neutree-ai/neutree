package e2e

import (
	"net/http"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// --- K8s Endpoint with Model Cache ---

var _ = Describe("K8s Endpoint Model Cache", Ordered, Label("endpoint", "k8s", "model-cache"), func() {
	var ClusterH *ClusterHelper

	BeforeAll(func() {
		if profileModelName() == "" {
			Skip("Model name not configured in profile, skipping model cache endpoint tests")
		}

		requireImageRegistryProfile()

		By("Setting up image registry")
		SetupImageRegistry()
		ClusterH = NewClusterHelper()

		By("Setting up model registry")
		SetupModelRegistry()
	})

	AfterAll(func() {
		TeardownModelRegistry()
		TeardownImageRegistry()
	})

	// --- NFS Model Cache ---

	Describe("NFS Cache", Ordered, Label("nfs"), func() {
		var (
			clusterName string
			epName      string
		)

		BeforeAll(func() {
			if profile.ModelCache.NFSServer == "" {
				Skip("ModelCache.NFSServer not configured in profile")
			}

			kubeconfig := requireK8sProfile()
			clusterName = "e2e-mc-nfs-k8s-" + Cfg.RunID
			epName = "e2e-ep-mc-nfs-" + Cfg.RunID

			yaml := renderK8sClusterYAML(map[string]any{
				"name":       clusterName,
				"kubeconfig": kubeconfig,
				"model_caches": []ModelCache{
					{Name: "nfs-cache", Mode: "nfs", NFSServer: profile.ModelCache.NFSServer, NFSPath: profile.ModelCache.NFSPath},
				},
			})

			r := ClusterH.Apply(yaml)
			ExpectSuccess(r)

			r = ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
			ExpectSuccess(r)
		})

		AfterAll(func() {
			deleteEndpoint(epName)
			ClusterH.EnsureDeleted(clusterName)
		})

		It("should deploy endpoint and reach Running", func() {
			yamlPath := applyEndpoint(epName, clusterName)
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Running"))
		})

		It("should serve inference requests", func() {
			ep := getEndpoint(epName)
			code, body, err := inferChat(ep.Status.ServiceURL, "Hello with NFS cache")
			Expect(err).NotTo(HaveOccurred())
			Expect(code).To(Equal(http.StatusOK), "inference with NFS cache failed: %s", body)
			Expect(body).To(ContainSubstring("choices"))
		})
	})

	// --- HostPath Model Cache ---

	Describe("HostPath Cache", Ordered, Label("hostpath"), func() {
		var (
			clusterName string
			epName      string
		)

		BeforeAll(func() {
			if profile.ModelCache.HostPath == "" {
				Skip("ModelCache.HostPath not configured in profile")
			}

			kubeconfig := requireK8sProfile()
			clusterName = "e2e-mc-hp-k8s-" + Cfg.RunID
			epName = "e2e-ep-mc-hp-" + Cfg.RunID

			yaml := renderK8sClusterYAML(map[string]any{
				"name":       clusterName,
				"kubeconfig": kubeconfig,
				"model_caches": []ModelCache{
					{Name: "hp-cache", Mode: "host_path", HostPath: profile.ModelCache.HostPath},
				},
			})

			r := ClusterH.Apply(yaml)
			ExpectSuccess(r)

			r = ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
			ExpectSuccess(r)
		})

		AfterAll(func() {
			deleteEndpoint(epName)
			ClusterH.EnsureDeleted(clusterName)
		})

		It("should deploy endpoint and reach Running", func() {
			yamlPath := applyEndpoint(epName, clusterName)
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Running"))
		})

		It("should serve inference requests", func() {
			ep := getEndpoint(epName)
			code, body, err := inferChat(ep.Status.ServiceURL, "Hello with HostPath cache")
			Expect(err).NotTo(HaveOccurred())
			Expect(code).To(Equal(http.StatusOK), "inference with HostPath cache failed: %s", body)
			Expect(body).To(ContainSubstring("choices"))
		})
	})

	// --- PVC Model Cache ---

	Describe("PVC Cache", Ordered, Label("pvc"), func() {
		var (
			clusterName string
			epName      string
		)

		BeforeAll(func() {
			if profile.ModelCache.PVCStorageClass == "" {
				Skip("ModelCache.PVCStorageClass not configured in profile")
			}

			kubeconfig := requireK8sProfile()
			clusterName = "e2e-mc-pvc-k8s-" + Cfg.RunID
			epName = "e2e-ep-mc-pvc-" + Cfg.RunID

			yaml := renderK8sClusterYAML(map[string]any{
				"name":       clusterName,
				"kubeconfig": kubeconfig,
				"model_caches": []ModelCache{
					{Name: "pvc-cache", Mode: "pvc", PVCStorageClass: profile.ModelCache.PVCStorageClass, PVCStorage: "10Gi"},
				},
			})

			r := ClusterH.Apply(yaml)
			ExpectSuccess(r)

			r = ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
			ExpectSuccess(r)
		})

		AfterAll(func() {
			deleteEndpoint(epName)
			ClusterH.EnsureDeleted(clusterName)
		})

		It("should deploy endpoint and reach Running", func() {
			yamlPath := applyEndpoint(epName, clusterName)
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Running"))
		})

		It("should serve inference requests", func() {
			ep := getEndpoint(epName)
			code, body, err := inferChat(ep.Status.ServiceURL, "Hello with PVC cache")
			Expect(err).NotTo(HaveOccurred())
			Expect(code).To(Equal(http.StatusOK), "inference with PVC cache failed: %s", body)
			Expect(body).To(ContainSubstring("choices"))
		})
	})
})

// --- SSH Endpoint with Model Cache ---

var _ = Describe("SSH Endpoint Model Cache", Ordered, Label("endpoint", "ssh", "model-cache"), func() {
	var ClusterH *ClusterHelper

	BeforeAll(func() {
		if profileModelName() == "" {
			Skip("Model name not configured in profile, skipping SSH model cache endpoint tests")
		}

		requireImageRegistryProfile()

		By("Setting up image registry")
		SetupImageRegistry()
		ClusterH = NewClusterHelper()

		By("Setting up model registry")
		SetupModelRegistry()
	})

	AfterAll(func() {
		TeardownModelRegistry()
		TeardownImageRegistry()
	})

	// --- HostPath Model Cache ---

	Describe("HostPath Cache", Ordered, Label("hostpath"), func() {
		var (
			clusterName string
			epName      string
		)

		BeforeAll(func() {
			if profile.ModelCache.HostPath == "" {
				Skip("ModelCache.HostPath not configured in profile")
			}

			headIP, workerIPs, sshUser, sshPrivateKey := requireSSHProfile()
			clusterName = "e2e-mc-hp-ssh-" + Cfg.RunID
			epName = "e2e-ep-mc-hp-ssh-" + Cfg.RunID

			yaml := renderSSHClusterYAML(map[string]any{
				"name":            clusterName,
				"head_ip":         headIP,
				"worker_ips":      workerIPs,
				"ssh_user":        sshUser,
				"ssh_private_key": sshPrivateKey,
				"model_caches": []ModelCache{
					{Name: "hp-cache", Mode: "host_path", HostPath: profile.ModelCache.HostPath},
				},
			})

			r := ClusterH.Apply(yaml)
			ExpectSuccess(r)

			r = ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
			ExpectSuccess(r)
		})

		AfterAll(func() {
			deleteEndpoint(epName)
			ClusterH.EnsureDeleted(clusterName)
		})

		It("should deploy endpoint and reach Running", func() {
			yamlPath := applyEndpoint(epName, clusterName)
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Running"))
		})

		It("should serve inference requests", func() {
			ep := getEndpoint(epName)
			code, body, err := inferChat(ep.Status.ServiceURL, "Hello with SSH HostPath cache")
			Expect(err).NotTo(HaveOccurred())
			Expect(code).To(Equal(http.StatusOK), "inference with SSH HostPath cache failed: %s", body)
			Expect(body).To(ContainSubstring("choices"))
		})
	})

})
