package e2e

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Control Plane Cleanup", Ordered, Label("control-plane", "cleanup"), func() {
	var cph *CPHelper

	BeforeAll(func() {
		cfg := requireCPProfile()
		cph = NewCPHelper(cfg)

		By("Deploying CLI binary to machine")
		cph.DeployCLIBinary()
	})

	BeforeEach(func() {
		cph.CleanAll()

		By("Launching obs-stack first")
		r := cph.RunCLI(append([]string{"launch", "obs-stack"}, mirrorRegistryArgs()...)...)
		ExpectSuccess(r)

		By("Launching neutree-core")
		args := append([]string{"launch", "neutree-core",
			"--jwt-secret", "e2e-cleanup-jwt-secret-long-enough-" + Cfg.RunID,
			"--version", profileCPVersion(),
			"--admin-password", profile.Auth.Password,
			"--metrics-remote-write-url", cph.MetricsRemoteWriteURL(),
		}, mirrorRegistryArgs()...)
		r = cph.RunCLI(args...)
		ExpectSuccess(r)

		Eventually(cph.CanLogin, 5*time.Minute, 5*time.Second).Should(BeTrue(),
			"admin login should succeed after launch")
	})

	AfterEach(func() {
		cph.CleanAll()
	})

	It("should skip confirmation with --force", Label("C2642198"), func() {
		obs := cph.ParseCompose("obs-stack")

		r := cph.RunCLI("cleanup", "obs-stack", "--force")
		ExpectSuccess(r)

		cph.VerifyCleanup(obs, false)
	})

	It("should cleanup neutree-core preserving data volumes", Label("C2642195"), func() {
		core := cph.ParseCompose("neutree-core")

		r := cph.RunCLI("cleanup", "neutree-core", "--force")
		ExpectSuccess(r)

		cph.VerifyCleanup(core, false)
	})

	It("should cleanup neutree-core and remove data volumes", Label("C2642196"), func() {
		core := cph.ParseCompose("neutree-core")

		r := cph.RunCLI("cleanup", "neutree-core", "--force", "--remove-data")
		ExpectSuccess(r)

		cph.VerifyCleanup(core, true)
	})

	It("should cleanup obs-stack", Label("C2642197"), func() {
		obs := cph.ParseCompose("obs-stack")

		r := cph.RunCLI("cleanup", "obs-stack", "--force")
		ExpectSuccess(r)

		cph.VerifyCleanup(obs, false)
	})

	It("should report error when cleaning up a component that was not launched", Label("C2642199"), func() {
		By("Cleaning up the launched state first")
		cph.CleanAll()

		r := cph.RunCLI("cleanup", "neutree-core", "--force")
		Expect(r.ExitCode).NotTo(Equal(0),
			"cleanup of non-launched component should fail")
		Expect(r.Stderr+r.Stdout).To(
			ContainSubstring("compose file not found"),
			"error should mention compose file not found")
	})
})
