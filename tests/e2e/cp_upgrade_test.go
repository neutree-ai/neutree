package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

var _ = Describe("Control Plane Upgrade", Ordered, Label("control-plane", "upgrade"), func() {
	var (
		cph        *CPHelper
		jwtSecret  string
		oldVersion string
		newVersion string

		sshClusterName string
		k8sClusterName string
		sshEpName      string
		k8sEpName      string
		irName         string
		mrName         string

		// Saved Cfg values, restored in AfterAll after cleanup
		origServerURL string
		origAPIKey    string

		// Pre-upgrade snapshots for spec comparison
		preSSHCluster v1.Cluster
		preK8sCluster v1.Cluster
		preMR         v1.ModelRegistry
		preIR         v1.ImageRegistry

		// Pre-upgrade endpoint LastTransitionTime
		preSSHEpTransitionTime string
		preK8sEpTransitionTime string
	)

	BeforeAll(func() {
		cfg := requireCPProfile()
		cph = NewCPHelper(cfg)

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

		By("Cleaning machine to pristine state")
		cph.DeployCLIBinary()
		cph.CleanAll()

		By("Downloading old CLI binary")
		cph.DownloadOldCLI(profileCPOldCLIURL())

		By("Deploying control plane with old version: " + oldVersion)
		oldCLIArgs := append([]string{"launch", "neutree-core",
			"--jwt-secret", jwtSecret,
			"--version", oldVersion,
			"--admin-password", profile.Auth.Password,
		}, mirrorRegistryCombinedArg()...)
		r := cph.RunOldCLI(oldCLIArgs...)
		ExpectSuccess(r)

		By("Waiting for old version API to be reachable")
		Eventually(cph.CanLogin, 5*time.Minute, 5*time.Second).Should(BeTrue(),
			"admin login should succeed on old version")

		By("Logging in as admin and creating API key on old CP")
		origServerURL = Cfg.ServerURL
		origAPIKey = Cfg.APIKey
		Cfg.ServerURL = cph.APIURL()

		jwt, err := loginTestUser(cph.APIURL(), profile.Auth.Email, profile.Auth.Password)
		Expect(err).NotTo(HaveOccurred())
		Expect(jwt).NotTo(BeEmpty(), "admin login should return JWT")

		// Create a real API key (sk_xxx) via PostgREST RPC — works with both CLI and Kong
		apiKey := createAPIKey(cph.APIURL(), jwt, profileWorkspace(), "e2e-upgrade-key-"+Cfg.RunID)
		Cfg.APIKey = apiKey

		By("Creating test resources on old version")
		createUpgradeTestResources(irName, mrName,
			sshClusterName, k8sClusterName, sshEpName, k8sEpName)

		By("Capturing pre-upgrade resource snapshots")
		if profileSSHHeadIP() != "" {
			r := RunCLI("get", "cluster", sshClusterName, "-w", profileWorkspace(), "-o", "json")
			ExpectSuccess(r)
			preSSHCluster = parseClusterJSON(r.Stdout)
		}

		if profileKubeconfig() != "" {
			r := RunCLI("get", "cluster", k8sClusterName, "-w", profileWorkspace(), "-o", "json")
			ExpectSuccess(r)
			preK8sCluster = parseClusterJSON(r.Stdout)
		}

		r = RunCLI("get", "modelregistry", mrName, "-w", profileWorkspace(), "-o", "json")
		ExpectSuccess(r)
		Expect(json.Unmarshal([]byte(r.Stdout), &preMR)).To(Succeed())

		r = RunCLI("get", "imageregistry", irName, "-w", profileWorkspace(), "-o", "json")
		ExpectSuccess(r)
		Expect(json.Unmarshal([]byte(r.Stdout), &preIR)).To(Succeed())

		By("Capturing pre-upgrade endpoint snapshots")
		if profileSSHHeadIP() != "" && profileModelName() != "" && canDeploySSHEndpoint() {
			var ep v1.Endpoint

			r = RunCLI("get", "endpoint", sshEpName, "-w", profileWorkspace(), "-o", "json")
			ExpectSuccess(r)
			Expect(json.Unmarshal([]byte(r.Stdout), &ep)).To(Succeed())

			preSSHEpTransitionTime = ep.Status.LastTransitionTime
		}

		if profileKubeconfig() != "" && profileModelName() != "" && canDeployK8sEndpoint() {
			var ep v1.Endpoint

			r = RunCLI("get", "endpoint", k8sEpName, "-w", profileWorkspace(), "-o", "json")
			ExpectSuccess(r)
			Expect(json.Unmarshal([]byte(r.Stdout), &ep)).To(Succeed())

			preK8sEpTransitionTime = ep.Status.LastTransitionTime
		}

		By("Resources created on old version, ready for upgrade")
	})

	AfterAll(func() {
		if cph == nil {
			return
		}

		// Best-effort cleanup — guard with profile checks so we don't
		// call delete on resources that were never created.
		ch := NewClusterHelper()

		if profileSSHHeadIP() != "" {
			deleteEndpoint(sshEpName)
			ch.Delete(sshClusterName)
			ch.WaitForDelete(sshClusterName, 5*time.Minute)
		}

		if profileKubeconfig() != "" {
			deleteEndpoint(k8sEpName)
			ch.Delete(k8sClusterName)
			ch.WaitForDelete(k8sClusterName, 5*time.Minute)
		}

		RunCLI("delete", "imageregistry", irName,
			"-w", profileWorkspace(), "--force", "--ignore-not-found")
		RunCLI("delete", "modelregistry", mrName,
			"-w", profileWorkspace(), "--force", "--ignore-not-found")

		cph.CleanAll()

		// Restore Cfg after all cleanup commands that depend on the CP URL/API key.
		Cfg.ServerURL = origServerURL
		Cfg.APIKey = origAPIKey
	})

	// --- Upgrade action ---

	It("should upgrade control plane to new version", func() {
		By("Upgrading control plane: " + oldVersion + " → " + newVersion)
		upgradeArgs := append([]string{"launch", "neutree-core",
			"--jwt-secret", jwtSecret,
			"--version", newVersion,
			"--admin-password", profile.Auth.Password,
		}, mirrorRegistryArgs()...)
		r := cph.RunCLI(upgradeArgs...)
		ExpectSuccess(r)

		By("Waiting for new version API to be reachable")
		Eventually(cph.CanLogin, 5*time.Minute, 5*time.Second).Should(BeTrue(),
			"admin login should succeed after upgrade")

		By("Verifying API key still works after upgrade")
		r = RunCLI("get", "cluster", "-w", profileWorkspace())
		ExpectSuccess(r)
	})

	// --- Post-upgrade verification ---

	It("should preserve SSH cluster status and spec after upgrade", Label("C2578438", "C2624257"), func() {
		if profileSSHHeadIP() == "" {
			Skip("SSH not configured, skipping")
		}

		r := RunCLI("get", "cluster", sshClusterName, "-w", profileWorkspace(), "-o", "json")
		ExpectSuccess(r)

		c := parseClusterJSON(r.Stdout)
		Expect(c.Status.Phase).To(BeEquivalentTo("Running"),
			"SSH cluster should still be Running after upgrade")
		Expect(c.Spec).To(Equal(preSSHCluster.Spec),
			"SSH cluster spec should be unchanged after upgrade")
	})

	It("should preserve K8s cluster status and spec after upgrade", Label("C2578439", "C2624258"), func() {
		if profileKubeconfig() == "" {
			Skip("kubeconfig not configured, skipping K8s cluster upgrade check")
		}

		r := RunCLI("get", "cluster", k8sClusterName, "-w", profileWorkspace(), "-o", "json")
		ExpectSuccess(r)

		c := parseClusterJSON(r.Stdout)
		Expect(c.Status.Phase).To(BeEquivalentTo("Running"),
			"K8s cluster should still be Running after upgrade")
		Expect(c.Spec).To(Equal(preK8sCluster.Spec),
			"K8s cluster spec should be unchanged after upgrade")
	})

	It("should preserve model registry config after upgrade", Label("C2578440", "C2624259"), func() {
		r := RunCLI("get", "modelregistry", mrName, "-w", profileWorkspace(), "-o", "json")
		ExpectSuccess(r)

		var mr v1.ModelRegistry
		Expect(json.Unmarshal([]byte(r.Stdout), &mr)).To(Succeed())

		Expect(mr.Metadata.Name).To(Equal(mrName))
		Expect(mr.Spec).To(Equal(preMR.Spec),
			"model registry spec should be unchanged after upgrade")
	})

	It("should preserve image registry config after upgrade", Label("C2578441"), func() {
		r := RunCLI("get", "imageregistry", irName, "-w", profileWorkspace(), "-o", "json")
		ExpectSuccess(r)

		var ir v1.ImageRegistry
		Expect(json.Unmarshal([]byte(r.Stdout), &ir)).To(Succeed())

		Expect(ir.Metadata.Name).To(Equal(irName))
		Expect(ir.Spec).To(Equal(preIR.Spec),
			"image registry spec should be unchanged after upgrade")
	})

	It("should preserve SSH endpoint function after upgrade", Label("C2578443"), func() {
		if !canDeploySSHEndpoint() {
			Skip("SSH endpoint not supported for this engine/cluster version combination")
		}

		ep := getEndpoint(sshEpName)
		Expect(ep.Status.Phase).To(BeEquivalentTo("Running"),
			"SSH endpoint should still be Running after upgrade")

		By("Verifying endpoint was not redeployed during upgrade")
		if preSSHEpTransitionTime != "" {
			var epFull v1.Endpoint

			r := RunCLI("get", "endpoint", sshEpName, "-w", profileWorkspace(), "-o", "json")
			ExpectSuccess(r)
			Expect(json.Unmarshal([]byte(r.Stdout), &epFull)).To(Succeed())

			Expect(epFull.Status.LastTransitionTime).To(Equal(preSSHEpTransitionTime),
				"SSH endpoint LastTransitionTime should not change after CP upgrade")
		}

		By("Verifying inference still works (with retry for Kong recovery)")
		Eventually(func() error {
			code, body, err := inferChat(ep.Status.ServiceURL, "Hello after upgrade")
			if err != nil {
				return err
			}

			if code != http.StatusOK {
				return fmt.Errorf("inference returned HTTP %d: %s", code, body)
			}

			return nil
		}, 3*time.Minute, 10*time.Second).Should(Succeed(),
			"inference on SSH endpoint should succeed after upgrade")
	})

	It("should preserve K8s endpoint function after upgrade", Label("C2578444"), func() {
		if profileKubeconfig() == "" {
			Skip("kubeconfig not configured, skipping K8s endpoint upgrade check")
		}

		if !canDeployK8sEndpoint() {
			Skip("K8s endpoint not supported for this engine/cluster version combination")
		}

		ep := getEndpoint(k8sEpName)
		Expect(ep.Status.Phase).To(BeEquivalentTo("Running"),
			"K8s endpoint should still be Running after upgrade")

		By("Verifying endpoint was not redeployed during upgrade")
		if preK8sEpTransitionTime != "" {
			var epFull v1.Endpoint

			r := RunCLI("get", "endpoint", k8sEpName, "-w", profileWorkspace(), "-o", "json")
			ExpectSuccess(r)
			Expect(json.Unmarshal([]byte(r.Stdout), &epFull)).To(Succeed())

			Expect(epFull.Status.LastTransitionTime).To(Equal(preK8sEpTransitionTime),
				"K8s endpoint LastTransitionTime should not change after CP upgrade")
		}

		By("Verifying inference still works")
		Eventually(func() error {
			code, body, err := inferChat(ep.Status.ServiceURL, "Hello after K8s upgrade")
			if err != nil {
				return err
			}

			if code != http.StatusOK {
				return fmt.Errorf("inference returned HTTP %d: %s", code, body)
			}

			return nil
		}, 3*time.Minute, 10*time.Second).Should(Succeed(),
			"inference on K8s endpoint should succeed after upgrade")
	})

})

// --- Setup helpers ---

// createUpgradeTestResources creates all resources needed for upgrade verification.
// Caller must set Cfg.ServerURL and Cfg.APIKey to point to the target CP before calling.
func createUpgradeTestResources(irName, mrName, sshCluster, k8sCluster, sshEp, k8sEp string) {
	By("Creating image registry")

	irPath, err := renderTemplateToTempFile("testdata/image-registry.yaml", map[string]string{
		"E2E_IMAGE_REGISTRY": irName,
	})
	Expect(err).NotTo(HaveOccurred())

	defer os.Remove(irPath)

	r := RunCLI("apply", "-f", irPath)
	ExpectSuccess(r)

	r = RunCLI("wait", "imageregistry", irName,
		"-w", profileWorkspace(),
		"--for", "jsonpath=.status.phase=Connected",
		"--timeout", "2m",
	)
	ExpectSuccess(r)

	By("Creating model registry")

	mrPath, err := renderTemplateToTempFile("testdata/model-registry.yaml", map[string]string{
		"E2E_MODEL_REGISTRY": mrName,
	})
	Expect(err).NotTo(HaveOccurred())

	defer os.Remove(mrPath)

	r = RunCLI("apply", "-f", mrPath)
	ExpectSuccess(r)

	By("Creating SSH cluster with old cluster version")
	if profileSSHHeadIP() != "" {
		headIP, workerIPs, sshUser, sshPrivateKey := requireSSHProfile()
		yaml := renderSSHClusterYAML(map[string]string{
			"name":            sshCluster,
			"image_registry":  irName,
			"version":         profile.Cluster.OldVersion,
			"head_ip":         headIP,
			"worker_ips":      workerIPs,
			"ssh_user":        sshUser,
			"ssh_private_key": sshPrivateKey,
		})

		ch := NewClusterHelper()
		r := ch.Apply(yaml)
		ExpectSuccess(r)

		r = ch.WaitForPhase(sshCluster, "Running", 10*time.Minute)
		ExpectSuccess(r)

		By("Creating SSH endpoint with old engine version")
		if profileModelName() != "" && canDeploySSHEndpoint() {
			yamlPath := applyEndpoint(sshEp, sshCluster, withEngineVersion(profileEngineOldVersion()))
			defer os.Remove(yamlPath)

			waitEndpointRunning(sshEp)
		}
	}

	By("Creating K8s cluster with old cluster version")
	if profileKubeconfig() != "" {
		kubeconfig := requireK8sProfile()
		yaml := renderK8sClusterYAML(map[string]string{
			"name":           k8sCluster,
			"image_registry": irName,
			"version":        profile.Cluster.OldVersion,
			"kubeconfig":     kubeconfig,
		})

		ch := NewClusterHelper()
		r := ch.Apply(yaml)
		ExpectSuccess(r)

		r = ch.WaitForPhase(k8sCluster, "Running", 10*time.Minute)
		ExpectSuccess(r)

		By("Creating K8s endpoint with old engine version")
		if profileModelName() != "" && canDeployK8sEndpoint() {
			yamlPath := applyEndpoint(k8sEp, k8sCluster, withEngineVersion(profileEngineOldVersion()))
			defer os.Remove(yamlPath)

			waitEndpointRunning(k8sEp)
		}
	}
}

