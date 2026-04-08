package e2e

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Control Plane Cleanup", Ordered, Label("control-plane", "cleanup"), func() {
	var cp *CPHelper

	BeforeAll(func() {
		cp = requireCPEnv()

		By("Deploying CLI binary to VM")
		cp.DeployCLIBinary()

		By("Cleaning VM to pristine state")
		cp.CleanAll()

		By("Deploying neutree-core + obs-stack for cleanup tests")
		r := cp.LaunchVersion(false, profileCPVersion(), "e2e-cleanup-jwt-secret-long-enough-"+Cfg.RunID)
		ExpectCPSuccess(r)

		r = cp.RunCLI("launch", "obs-stack")
		ExpectCPSuccess(r)

		Eventually(func() int {
			r := cp.CurlAPI("/health")
			return r.ExitCode
		}, 5*time.Minute, 5*time.Second).Should(Equal(0))
	})

	AfterAll(func() {
		if cp != nil {
			cp.CleanAll()
		}
	})

	It("should skip confirmation with --force", Label("C2642198"), func() {
		r := cp.RunCLI("cleanup", "obs-stack", "--force")
		ExpectCPSuccess(r)

		By("Verifying obs-stack containers are stopped")
		Eventually(func() bool {
			return cp.CheckContainerRunning("grafana")
		}, 30*time.Second, 2*time.Second).Should(BeFalse(),
			"grafana should be stopped after cleanup")

		By("Re-launching obs-stack for subsequent tests")
		r = cp.RunCLI("launch", "obs-stack")
		ExpectCPSuccess(r)
	})

	It("should cleanup neutree-core preserving data volumes", Label("C2642195"), func() {
		By("Recording volumes before cleanup")
		volumesBefore := cp.ListVolumes()
		Expect(volumesBefore).NotTo(BeEmpty(), "should have volumes before cleanup")

		By("Running cleanup neutree-core (no --remove-data)")
		r := cp.RunCLI("cleanup", "neutree-core", "--force")
		ExpectCPSuccess(r)

		By("Verifying core containers are stopped")
		Eventually(func() bool {
			return cp.CheckContainerRunning("neutree-api")
		}, 30*time.Second, 2*time.Second).Should(BeFalse())

		By("Verifying data volumes are preserved")
		volumesAfter := cp.ListVolumes()
		Expect(volumesAfter).NotTo(BeEmpty(),
			"volumes should be preserved after cleanup without --remove-data")

		By("Re-launching neutree-core for subsequent tests")
		r = cp.LaunchVersion(false, profileCPVersion(), "e2e-cleanup-jwt-secret-long-enough-"+Cfg.RunID)
		ExpectCPSuccess(r)

		Eventually(func() int {
			r := cp.CurlAPI("/health")
			return r.ExitCode
		}, 5*time.Minute, 5*time.Second).Should(Equal(0))
	})

	It("should cleanup neutree-core and remove data volumes", Label("C2642196"), func() {
		By("Recording neutree-core volumes before cleanup")
		volumesBefore := cp.ListVolumes()

		By("Running cleanup with --remove-data")
		r := cp.RunCLI("cleanup", "neutree-core", "--force", "--remove-data")
		ExpectCPSuccess(r)

		By("Verifying core containers are stopped")
		Eventually(func() bool {
			return cp.CheckContainerRunning("neutree-api")
		}, 30*time.Second, 2*time.Second).Should(BeFalse())

		By("Verifying neutree-core volumes are removed")
		Expect(volumesBefore).NotTo(BeEmpty(),
			"there should be volumes before cleanup (precondition)")

		volumesAfter := cp.ListVolumes()
		Expect(len(volumesAfter)).To(BeNumerically("<", len(volumesBefore)),
			"volumes should decrease after --remove-data")
	})

	It("should cleanup obs-stack", Label("C2642197"), func() {
		r := cp.RunCLI("cleanup", "obs-stack", "--force")
		ExpectCPSuccess(r)

		By("Verifying monitoring containers are stopped")
		Eventually(func() bool {
			return cp.CheckContainerRunning("grafana")
		}, 30*time.Second, 2*time.Second).Should(BeFalse(),
			"grafana should be stopped after obs-stack cleanup")
	})

	It("should report error when cleaning up a component that was not launched", Label("C2642199"), func() {
		cp.RunSSHCmd("rm -rf " + profileCPComposeDir() + "/neutree-core")

		r := cp.RunCLI("cleanup", "neutree-core", "--force")
		Expect(r.ExitCode).NotTo(Equal(0),
			"cleanup of non-launched component should fail")
		Expect(r.Stderr + r.Stdout).To(
			ContainSubstring("compose file not found"),
			"error should mention compose file not found")
	})
})
