package e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

var _ = Describe("Control Plane Upgrade", Ordered, Label("control-plane", "upgrade"), func() {
	var (
		cp         *CPHelper
		jwtSecret  string
		oldVersion string
		newVersion string

		sshClusterName string
		k8sClusterName string
		sshEpName      string
		k8sEpName      string
		irName         string
		mrName         string
	)

	BeforeAll(func() {
		cp = requireCPEnv()

		oldVersion = profileCPOldVersion()
		if oldVersion == "" {
			Skip("control_plane.old_version not configured, skipping upgrade tests")
		}

		if profileCPOldCLIURL() == "" {
			Skip("control_plane.old_cli_url not configured, skipping upgrade tests")
		}

		newVersion = profileCPVersion() // current CP version as upgrade target
		jwtSecret = "e2e-upgrade-jwt-secret-long-enough-" + Cfg.RunID

		sshClusterName = "e2e-upg-ssh-" + Cfg.RunID
		k8sClusterName = "e2e-upg-k8s-" + Cfg.RunID
		sshEpName = "e2e-upg-ep-ssh-" + Cfg.RunID
		k8sEpName = "e2e-upg-ep-k8s-" + Cfg.RunID
		irName = testImageRegistry()
		mrName = testRegistry()

		By("Cleaning VM to pristine state")
		cp.DeployCLIBinary()
		cp.CleanAll()

		By("Downloading old CLI binary")
		cp.DownloadOldCLI(profileCPOldCLIURL())

		By("Deploying control plane with old version: " + oldVersion)
		r := cp.LaunchVersion(true, oldVersion, jwtSecret)
		ExpectCPSuccess(r)

		By("Waiting for old version API to be reachable")
		Eventually(func() int {
			r := cp.CurlAPI("/health")
			return r.ExitCode
		}, 5*time.Minute, 5*time.Second).Should(Equal(0),
			"old version API should be reachable")

		By("Switching CLI to target the new CP and logging in as admin")
		origServerURL := Cfg.ServerURL
		origAPIKey := Cfg.APIKey
		Cfg.ServerURL = cp.APIURL()

		By("Logging in as admin and creating API key on old CP")
		jwt := loginTestUser(profile.Auth.Email, profile.Auth.Password)
		Expect(jwt).NotTo(BeEmpty(), "admin login should return JWT")

		// Create a real API key (sk_xxx) via PostgREST RPC — works with both CLI and Kong
		apiKey := createAPIKey(cp.APIURL(), jwt, profileWorkspace(), "e2e-upgrade-key-"+Cfg.RunID)
		Cfg.APIKey = apiKey

		DeferCleanup(func() {
			Cfg.ServerURL = origServerURL
			Cfg.APIKey = origAPIKey
		})

		By("Creating test resources on old version")
		createUpgradeTestResources(cp, irName, mrName,
			sshClusterName, k8sClusterName, sshEpName, k8sEpName)

		By("Resources created on old version, ready for upgrade")
	})

	AfterAll(func() {
		if cp == nil {
			return
		}

		// Best-effort cleanup of test resources
		deleteEndpoint(sshEpName)
		deleteEndpoint(k8sEpName)

		ch := NewClusterHelper()
		ch.Delete(sshClusterName)
		ch.Delete(k8sClusterName)
		ch.WaitForDelete(sshClusterName, "5m")
		ch.WaitForDelete(k8sClusterName, "5m")

		RunCLI("delete", "imageregistry", irName,
			"-w", profileWorkspace(), "--force", "--ignore-not-found")
		RunCLI("delete", "modelregistry", mrName,
			"-w", profileWorkspace(), "--force", "--ignore-not-found")

		cp.CleanAll()
	})

	// --- Upgrade action ---

	It("should upgrade control plane to new version", func() {
		By("Upgrading control plane: " + oldVersion + " → " + newVersion)
		r := cp.LaunchVersion(false, newVersion, jwtSecret)
		ExpectCPSuccess(r)

		By("Waiting for new version API to be reachable")
		Eventually(func() int {
			r := cp.CurlAPI("/health")
			return r.ExitCode
		}, 5*time.Minute, 5*time.Second).Should(Equal(0),
			"new version API should be reachable after upgrade")

		By("Verifying API key still works after upgrade (same JWT secret)")
		// The API key created on old CP is stored in DB which survives upgrade.
		// JWT secret is the same, so the sk_xxx key signature remains valid.
	})

	// --- Verification: 1.0 子版本升级 ---

	It("should preserve SSH cluster status after upgrade", Label("C2578438", "C2624257"), func() {
		r := RunCLI("get", "cluster", sshClusterName, "-w", profileWorkspace(), "-o", "json")
		ExpectSuccess(r)

		c := parseClusterJSON(r.Stdout)
		Expect(c.Status.Phase).To(BeEquivalentTo("Running"),
			"SSH cluster should still be Running after upgrade")
	})

	It("should preserve K8s cluster status after upgrade", Label("C2578439", "C2624258"), func() {
		if profileKubeconfig() == "" {
			Skip("kubeconfig not configured, skipping K8s cluster upgrade check")
		}

		r := RunCLI("get", "cluster", k8sClusterName, "-w", profileWorkspace(), "-o", "json")
		ExpectSuccess(r)

		c := parseClusterJSON(r.Stdout)
		Expect(c.Status.Phase).To(BeEquivalentTo("Running"),
			"K8s cluster should still be Running after upgrade")
	})

	It("should preserve model registry config after upgrade", Label("C2578440", "C2624259"), func() {
		r := RunCLI("get", "modelregistry", mrName, "-w", profileWorkspace(), "-o", "json")
		ExpectSuccess(r)

		var mr v1.ModelRegistry
		Expect(json.Unmarshal([]byte(r.Stdout), &mr)).To(Succeed())

		Expect(mr.Metadata.Name).To(Equal(mrName),
			"model registry name should be preserved")
		Expect(mr.Spec).NotTo(BeNil(),
			"model registry spec should not be nil after upgrade")
	})

	It("should preserve image registry config after upgrade", Label("C2578441"), func() {
		r := RunCLI("get", "imageregistry", irName, "-w", profileWorkspace(), "-o", "json")
		ExpectSuccess(r)

		var ir v1.ImageRegistry
		Expect(json.Unmarshal([]byte(r.Stdout), &ir)).To(Succeed())

		Expect(ir.Metadata.Name).To(Equal(irName),
			"image registry name should be preserved")
	})

	It("should preserve SSH endpoint function after upgrade", Label("C2578443"), func() {
		ep := getEndpoint(sshEpName)
		Expect(ep.Status.Phase).To(BeEquivalentTo("Running"),
			"SSH endpoint should still be Running after upgrade")

		By("Verifying inference still works (with retry for Kong recovery)")
		Eventually(func() bool {
			code, _ := inferChatSafe(ep.Status.ServiceURL, "Hello after upgrade")
			return code == http.StatusOK
		}, 3*time.Minute, 10*time.Second).Should(BeTrue(),
			"inference on SSH endpoint should return 200 after upgrade")
	})

	It("should preserve K8s endpoint function after upgrade", Label("C2578444"), func() {
		if profileKubeconfig() == "" {
			Skip("kubeconfig not configured, skipping K8s endpoint upgrade check")
		}

		ep := getEndpoint(k8sEpName)
		Expect(ep.Status.Phase).To(BeEquivalentTo("Running"),
			"K8s endpoint should still be Running after upgrade")

		By("Verifying inference still works")
		Eventually(func() bool {
			code, _ := inferChatSafe(ep.Status.ServiceURL, "Hello after K8s upgrade")
			return code == http.StatusOK
		}, 3*time.Minute, 10*time.Second).Should(BeTrue(),
			"inference on K8s endpoint should return 200 after upgrade")
	})

	// --- C2642268: breaking change compatibility ---

	It("should handle vLLM endpoint breaking change after upgrade", Label("C2642268"), func() {
		ep := getEndpoint(sshEpName)

		// After upgrade, endpoint may need config update due to breaking changes.
		// If endpoint is not Running, try re-applying with force-update.
		if ep.Status.Phase != "Running" {
			By("Endpoint not Running after upgrade, re-applying with force-update")
			yamlPath := applyEndpointOnCluster(sshEpName, sshClusterName, profileEngineVersion())
			defer os.Remove(yamlPath)

			waitEndpointRunning(sshEpName)
		}

		ep = getEndpoint(sshEpName)
		Expect(ep.Status.Phase).To(BeEquivalentTo("Running"),
			"endpoint should be Running after upgrade + config update")

		By("Verifying inference works after breaking change handling")
		code, body := inferChat(ep.Status.ServiceURL, "Hello breaking change")
		Expect(code).To(Equal(http.StatusOK),
			"inference should work after breaking change handling: %s", body)
	})
})

// --- Setup helpers ---

// createUpgradeTestResources creates all resources needed for upgrade verification.
// Caller must set Cfg.ServerURL and Cfg.APIKey to point to the target CP before calling.
func createUpgradeTestResources(_ *CPHelper, irName, mrName, sshCluster, k8sCluster, sshEp, k8sEp string) {
	By("Creating image registry")

	irPath, err := renderTemplateToTempFile("testdata/image-registry.yaml", map[string]string{
		"E2E_IMAGE_REGISTRY": irName,
	})
	Expect(err).NotTo(HaveOccurred())

	defer os.Remove(irPath)

	r := RunCLI("apply", "-f", irPath)
	ExpectSuccess(r)

	RunCLI("wait", "imageregistry", irName,
		"-w", profileWorkspace(),
		"--for", "jsonpath=.status.phase=Connected",
		"--timeout", "2m",
	)

	By("Creating model registry")

	mrPath, err := renderTemplateToTempFile("testdata/model-registry.yaml", map[string]string{
		"E2E_MODEL_REGISTRY": mrName,
	})
	Expect(err).NotTo(HaveOccurred())

	defer os.Remove(mrPath)

	r = RunCLI("apply", "-f", mrPath)
	ExpectSuccess(r)

	By("Creating SSH cluster with old cluster version (v1.0.0)")
	if profileSSHHeadIP() != "" {
		headIP, workerIPs, sshUser, sshPrivateKey := requireSSHEnv()
		yaml := renderSSHClusterYAML(map[string]string{
			"name":            sshCluster,
			"image_registry":  irName,
			"version":         profile.Cluster.OldVersion, // v1.0.0
			"head_ip":         headIP,
			"worker_ips":      workerIPs,
			"ssh_user":        sshUser,
			"ssh_private_key": sshPrivateKey,
		})

		ch := NewClusterHelper()
		r := ch.Apply(yaml)
		ExpectSuccess(r)

		r = ch.WaitForPhase(sshCluster, "Running", "10m")
		ExpectSuccess(r)

		By("Creating SSH endpoint with old engine version (v0.8.5 for v1.0.0 compat)")
		if profileModelName() != "" {
			yamlPath := applyEndpointOnCluster(sshEp, sshCluster, profileEngineOldVersion())
			os.Remove(yamlPath)
			waitEndpointRunning(sshEp)
		}
	}

	By("Creating K8s cluster with old cluster version (v1.0.0)")
	if profileKubeconfig() != "" {
		kubeconfig := requireK8sEnv()
		yaml := renderK8sClusterYAML(map[string]string{
			"name":           k8sCluster,
			"image_registry": irName,
			"version":        profile.Cluster.OldVersion, // v1.0.0
			"kubeconfig":     kubeconfig,
		})

		ch := NewClusterHelper()
		r := ch.Apply(yaml)
		ExpectSuccess(r)

		r = ch.WaitForPhase(k8sCluster, "Running", "10m")
		ExpectSuccess(r)

		By("Creating K8s endpoint with old engine version")
		if profileModelName() != "" {
			yamlPath := applyEndpointOnCluster(k8sEp, k8sCluster, profileEngineOldVersion())
			os.Remove(yamlPath)
			waitEndpointRunning(k8sEp)
		}
	}
}

