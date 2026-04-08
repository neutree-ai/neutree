package e2e

import (
	"os"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Image Registry", Label("image-registry"), func() {

	Describe("No-Protocol URL", Label("no-protocol"), func() {
		It("should create image registry with URL without protocol prefix", Label("C2642281"), func() {
			requireImageRegistryEnv()

			name := "e2e-ir-noproto-" + Cfg.RunID
			rawURL := profile.ImageRegistry.URL
			rawURL = strings.TrimPrefix(rawURL, "https://")
			rawURL = strings.TrimPrefix(rawURL, "http://")

			Expect(rawURL).NotTo(BeEmpty(), "stripped registry URL should not be empty")

			yamlPath, err := renderTemplateToTempFile("testdata/image-registry.yaml", map[string]string{
				"E2E_IMAGE_REGISTRY":     name,
				"E2E_IMAGE_REGISTRY_URL": rawURL,
			})
			Expect(err).NotTo(HaveOccurred())
			defer os.Remove(yamlPath)

			DeferCleanup(func() {
				RunCLI("delete", "imageregistry", name,
					"-w", profileWorkspace(), "--force", "--ignore-not-found")
			})

			By("Applying image registry with no-protocol URL: " + rawURL)
			r := RunCLI("apply", "-f", yamlPath)
			ExpectSuccess(r)

			By("Waiting for Connected phase")
			r = RunCLI("wait", "imageregistry", name,
				"-w", profileWorkspace(),
				"--for", "jsonpath=.status.phase=Connected",
				"--timeout", "2m",
			)
			ExpectSuccess(r)
		})
	})

	Describe("No-Protocol URL Endpoint Deploy", Ordered, Label("no-protocol", "deploy"), func() {
		var (
			irName      string
			clusterName string
			epName      string
		)

		BeforeAll(func() {
			requireImageRegistryEnv()
			if profileModelName() == "" {
				Skip("Model name not configured in profile, skipping endpoint deploy test")
			}

			irName = "e2e-ir-noproto-ep-" + Cfg.RunID
			clusterName = "e2e-cl-noproto-" + Cfg.RunID
			epName = "e2e-ep-noproto-" + Cfg.RunID

			// Strip protocol from registry URL
			rawURL := profile.ImageRegistry.URL
			rawURL = strings.TrimPrefix(rawURL, "https://")
			rawURL = strings.TrimPrefix(rawURL, "http://")

			By("Creating image registry with no-protocol URL: " + rawURL)

			yamlPath, err := renderTemplateToTempFile("testdata/image-registry.yaml", map[string]string{
				"E2E_IMAGE_REGISTRY":     irName,
				"E2E_IMAGE_REGISTRY_URL": rawURL,
			})
			Expect(err).NotTo(HaveOccurred())
			defer os.Remove(yamlPath)

			r := RunCLI("apply", "-f", yamlPath)
			ExpectSuccess(r)

			r = RunCLI("wait", "imageregistry", irName,
				"-w", profileWorkspace(),
				"--for", "jsonpath=.status.phase=Connected",
				"--timeout", "2m",
			)
			ExpectSuccess(r)

			By("Creating SSH cluster using no-protocol image registry")
			headIP, workerIPs, sshUser, sshPrivateKey := requireSSHEnv()
			clusterYAML := renderSSHClusterYAML(map[string]string{
				"name":            clusterName,
				"image_registry":  irName,
				"head_ip":         headIP,
				"worker_ips":      workerIPs,
				"ssh_user":        sshUser,
				"ssh_private_key": sshPrivateKey,
			})

			ch := NewClusterHelper()
			r = ch.Apply(clusterYAML)
			ExpectSuccess(r)

			r = ch.WaitForPhase(clusterName, "Running", "10m")
			ExpectSuccess(r)

			By("Setting up model registry")
			SetupModelRegistry()
		})

		AfterAll(func() {
			if epName != "" {
				deleteEndpoint(epName)
			}
			if clusterName != "" {
				ch := NewClusterHelper()
				ch.Delete(clusterName)
				ch.WaitForDelete(clusterName, "10m")
			}
			TeardownModelRegistry()
			if irName != "" {
				RunCLI("delete", "imageregistry", irName,
					"-w", profileWorkspace(), "--force", "--ignore-not-found")
			}
		})

		It("should deploy endpoint and reach Running with no-protocol registry", Label("C2642282"), func() {
			yamlPath := applyEndpointOnCluster(epName, clusterName, profileEngineVersion())
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Running"),
				"endpoint should reach Running, proving image pull works with no-protocol registry")
		})
	})

	Describe("Authenticated Registry", Label("auth"), func() {
		It("should create and connect with username and password", Label("C2612048"), func() {
			requireImageRegistryEnv()

			if profile.ImageRegistry.Username == "" || profile.ImageRegistry.Password == "" {
				Skip("ImageRegistry username/password not configured in profile")
			}

			name := "e2e-ir-auth-" + Cfg.RunID

			yamlPath, err := renderTemplateToTempFile("testdata/image-registry.yaml", map[string]string{
				"E2E_IMAGE_REGISTRY": name,
			})
			Expect(err).NotTo(HaveOccurred())
			defer os.Remove(yamlPath)

			DeferCleanup(func() {
				RunCLI("delete", "imageregistry", name,
					"-w", profileWorkspace(), "--force", "--ignore-not-found")
			})

			By("Applying image registry with auth credentials")
			r := RunCLI("apply", "-f", yamlPath)
			ExpectSuccess(r)

			By("Waiting for Connected phase")
			r = RunCLI("wait", "imageregistry", name,
				"-w", profileWorkspace(),
				"--for", "jsonpath=.status.phase=Connected",
				"--timeout", "2m",
			)
			ExpectSuccess(r)
		})
	})
})
