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
			requireImageRegistryProfile()

			name := "e2e-ir-noproto-" + Cfg.RunID
			rawURL := profile.ImageRegistry.URL
			rawURL = strings.TrimPrefix(rawURL, "https://")
			rawURL = strings.TrimPrefix(rawURL, "http://")

			Expect(rawURL).NotTo(BeEmpty(), "stripped registry URL should not be empty")

			yamlPath, err := renderTemplateToTempFile("testdata/image-registry.yaml", map[string]any{
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

	Describe("Authenticated Registry", Label("auth"), func() {
		It("should fail without credentials on auth-required registry", Label("C2612048"), func() {
			requireImageRegistryProfile()

			if profile.ImageRegistry.Username == "" || profile.ImageRegistry.Password == "" {
				Skip("ImageRegistry username/password not configured in profile")
			}

			noAuthName := "e2e-ir-noauth-" + Cfg.RunID
			authName := "e2e-ir-auth-" + Cfg.RunID

			DeferCleanup(func() {
				RunCLI("delete", "imageregistry", noAuthName,
					"-w", profileWorkspace(), "--force", "--ignore-not-found")
				RunCLI("delete", "imageregistry", authName,
					"-w", profileWorkspace(), "--force", "--ignore-not-found")
			})

			By("Creating image registry WITHOUT credentials")
			noAuthPath, err := renderTemplateToTempFile("testdata/image-registry.yaml", map[string]any{
				"E2E_IMAGE_REGISTRY":          noAuthName,
				"E2E_IMAGE_REGISTRY_USERNAME": "",
				"E2E_IMAGE_REGISTRY_PASSWORD": "",
			})
			Expect(err).NotTo(HaveOccurred())
			defer os.Remove(noAuthPath)

			r := RunCLI("apply", "-f", noAuthPath)
			ExpectSuccess(r)

			By("Verifying registry does NOT reach Connected without credentials")
			r = RunCLI("wait", "imageregistry", noAuthName,
				"-w", profileWorkspace(),
				"--for", "jsonpath=.status.phase=Connected",
				"--timeout", "30s",
			)
			ExpectFailed(r)

			By("Creating image registry WITH credentials")
			authPath, err := renderTemplateToTempFile("testdata/image-registry.yaml", map[string]any{
				"E2E_IMAGE_REGISTRY": authName,
			})
			Expect(err).NotTo(HaveOccurred())
			defer os.Remove(authPath)

			r = RunCLI("apply", "-f", authPath)
			ExpectSuccess(r)

			By("Verifying registry reaches Connected with credentials")
			r = RunCLI("wait", "imageregistry", authName,
				"-w", profileWorkspace(),
				"--for", "jsonpath=.status.phase=Connected",
				"--timeout", "2m",
			)
			ExpectSuccess(r)
		})
	})
})
