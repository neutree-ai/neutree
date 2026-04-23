package e2e

import (
	"fmt"
	"net/http"
	"os"
	"strings"

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

			nfsCacheYAML := fmt.Sprintf(`    model_caches:
      - name: nfs-cache
        nfs:
          server: "%s"
          path: "%s"`,
				profile.ModelCache.NFSServer,
				profile.ModelCache.NFSPath)

			yaml := renderK8sClusterYAML(map[string]string{
				"name":              clusterName,
				"kubeconfig":        kubeconfig,
				"model_caches_yaml": nfsCacheYAML,
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
			yamlPath := applyEndpointOnCluster(epName, clusterName, profileEngineVersion())
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Running"))
		})

		It("should serve inference requests", func() {
			ep := getEndpoint(epName)
			code, body := inferChat(ep.Status.ServiceURL, "Hello with NFS cache")
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

			hostPathYAML := fmt.Sprintf(`    model_caches:
      - name: hp-cache
        host_path:
          path: "%s"`, profile.ModelCache.HostPath)

			yaml := renderK8sClusterYAML(map[string]string{
				"name":              clusterName,
				"kubeconfig":        kubeconfig,
				"model_caches_yaml": hostPathYAML,
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
			yamlPath := applyEndpointOnCluster(epName, clusterName, profileEngineVersion())
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Running"))
		})

		It("should serve inference requests", func() {
			ep := getEndpoint(epName)
			code, body := inferChat(ep.Status.ServiceURL, "Hello with HostPath cache")
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

			pvcYAML := fmt.Sprintf(`    model_caches:
      - name: pvc-cache
        pvc:
          storageClassName: "%s"
          resources:
            requests:
              storage: 10Gi`, profile.ModelCache.PVCStorageClass)

			yaml := renderK8sClusterYAML(map[string]string{
				"name":              clusterName,
				"kubeconfig":        kubeconfig,
				"model_caches_yaml": pvcYAML,
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
			yamlPath := applyEndpointOnCluster(epName, clusterName, profileEngineVersion())
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Running"))
		})

		It("should serve inference requests", func() {
			ep := getEndpoint(epName)
			code, body := inferChat(ep.Status.ServiceURL, "Hello with PVC cache")
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

			hostPathYAML := fmt.Sprintf("    model_caches:\n      - name: hp-cache\n        host_path:\n          path: \"%s\"\n",
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

			r = ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
			ExpectSuccess(r)
		})

		AfterAll(func() {
			deleteEndpoint(epName)
			ClusterH.EnsureDeleted(clusterName)
		})

		It("should deploy endpoint and reach Running", func() {
			yamlPath := applyEndpointOnCluster(epName, clusterName, profileEngineVersion())
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Running"))
		})

		It("should serve inference requests", func() {
			ep := getEndpoint(epName)
			code, body := inferChat(ep.Status.ServiceURL, "Hello with SSH HostPath cache")
			Expect(code).To(Equal(http.StatusOK), "inference with SSH HostPath cache failed: %s", body)
			Expect(body).To(ContainSubstring("choices"))
		})
	})

	// --- NFS Model Cache (SSH) ---

	Describe("NFS Cache", Ordered, Label("nfs"), func() {
		var (
			clusterName string
			epName      string
		)

		BeforeAll(func() {
			if profile.ModelCache.NFSServer == "" {
				Skip("ModelCache.NFSServer not configured in profile")
			}

			headIP, workerIPs, sshUser, sshPrivateKey := requireSSHProfile()
			clusterName = "e2e-mc-nfs-ssh-" + Cfg.RunID
			epName = "e2e-ep-mc-nfs-ssh-" + Cfg.RunID

			nfsCacheYAML := fmt.Sprintf("    model_caches:\n      - name: nfs-cache\n        nfs:\n          server: \"%s\"\n          path: \"%s\"\n",
				profile.ModelCache.NFSServer,
				profile.ModelCache.NFSPath)

			yaml := renderSSHClusterYAML(map[string]string{
				"name":              clusterName,
				"head_ip":           headIP,
				"worker_ips":        workerIPs,
				"ssh_user":          sshUser,
				"ssh_private_key":   sshPrivateKey,
				"model_caches_yaml": nfsCacheYAML,
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

		It("should deploy endpoint with NFS cache and reach Running", Label("C2644068"), func() {
			yamlPath := applyEndpointOnCluster(epName, clusterName, profileEngineVersion())
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Running"))

			By("Verifying NFS mount uses type=nfs (not nfs4) via Ray Serve backend_container config")
			c := getClusterFullJSON(clusterName)
			rayH := NewRayHelper(c.Status.DashboardURL)
			apps, err := rayH.GetServeApplications()
			Expect(err).NotTo(HaveOccurred())

			foundNFSMount := false
			for _, appStatus := range apps.Applications {
				if appStatus.DeployedAppConfig == nil || appStatus.DeployedAppConfig.Args == nil {
					continue
				}
				bc, ok := appStatus.DeployedAppConfig.Args["backend_container"].(map[string]interface{})
				if !ok {
					continue
				}
				runOpts, ok := bc["run_options"].([]interface{})
				if !ok {
					continue
				}
				for _, opt := range runOpts {
					optStr, ok := opt.(string)
					if !ok {
						continue
					}
					if !strings.Contains(optStr, "--mount") || !strings.Contains(optStr, "type=nfs") {
						continue
					}
					foundNFSMount = true
					// Must use type=nfs, NOT type=nfs4
					// (nfs4 filesystem type was removed in kernel 5.6+)
					Expect(optStr).To(ContainSubstring("volume-opt=type=nfs"),
						"NFS mount should use type=nfs, not type=nfs4")
					Expect(optStr).NotTo(ContainSubstring("type=nfs4"),
						"NFS mount must not use type=nfs4 (removed in kernel 5.6+)")
					// NFSv4+ includes explicit nfsvers=N; NFSv3 omits it (kernel default).
					// Both are valid — the key check is type=nfs above.
				}
			}
			Expect(foundNFSMount).To(BeTrue(),
				"should find NFS --mount option in backend_container run_options")
		})

		It("should serve inference requests with NFS cache", func() {
			ep := getEndpoint(epName)
			code, body := inferChat(ep.Status.ServiceURL, "Hello with SSH NFS cache")
			Expect(code).To(Equal(http.StatusOK), "inference with SSH NFS cache failed: %s", body)
			Expect(body).To(ContainSubstring("choices"))
		})
	})

})
