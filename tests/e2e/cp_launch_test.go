package e2e

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Control Plane Launch Params", Ordered, Label("control-plane", "launch"), func() {
	var cp *CPHelper

	BeforeAll(func() {
		cp = requireCPEnv()

		By("Deploying CLI binary to VM")
		cp.DeployCLIBinary()

		By("Cleaning VM to pristine state")
		cp.CleanAll()
	})

	AfterAll(func() {
		if cp != nil {
			cp.CleanAll()
		}
	})

	It("should replace image addresses with --mirror-registry and --registry-project", Label("C2642203"), func() {
		mirrorRegistry := "my-mirror.example.com:5000"
		registryProject := "custom-project"

		By("Launching with --mirror-registry and --registry-project (dry-run)")
		r := cp.RunCLI("launch", "neutree-core",
			"--version", profileCPVersion(),
			"--jwt-secret", "e2e-mirror-jwt-secret-long-enough-"+Cfg.RunID,
			"--mirror-registry", mirrorRegistry,
			"--registry-project", registryProject,
			"--dry-run",
		)
		// dry-run generates compose file and prints content without executing docker compose up

		By("Checking generated compose file for correct image addresses")
		// dry-run prints compose content to stdout; also check the file on disk
		composeContent := r.Stdout
		if composeContent == "" {
			r2 := cp.RunSSHCmd("cat " + profileCPComposeDir() + "/neutree-core/docker-compose.yml 2>/dev/null")
			composeContent = r2.Stdout
		}

		Expect(composeContent).NotTo(BeEmpty(),
			"compose file should be generated")

		// Images should contain the mirror registry and project
		Expect(composeContent).To(ContainSubstring(mirrorRegistry),
			"compose file should reference mirror registry")
		Expect(composeContent).To(ContainSubstring(registryProject),
			"compose file should reference registry project")
	})
})
