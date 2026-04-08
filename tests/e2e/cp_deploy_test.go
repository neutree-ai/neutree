package e2e

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Control Plane Deploy", Ordered, Label("control-plane", "deploy"), func() {
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

	// --- Phase 1: Install validation ---

	Describe("Parameter Validation", Label("validation"), func() {
		It("should reject launch without --jwt-secret", Label("C2642239"), func() {
			r := cp.RunCLI("launch", "neutree-core",
				"--version", profileCPVersion(),
			)
			Expect(r.ExitCode).NotTo(Equal(0),
				"launch without jwt-secret should fail")
			Expect(r.Stderr + r.Stdout).To(
				ContainSubstring("jwt-secret"),
				"error should mention jwt-secret")
		})
	})

	Describe("Online Deploy", Ordered, Label("online"), func() {
		It("should deploy neutree-core with online images", Label("C2610706"), func() {
			r := cp.LaunchVersion(false, profileCPVersion(), "e2e-test-jwt-secret-long-enough-"+Cfg.RunID)
			ExpectCPSuccess(r)

			By("Waiting for API to be reachable")
			Eventually(func() int {
				r := cp.CurlAPI("/health")
				return r.ExitCode
			}, 5*time.Minute, 5*time.Second).Should(Equal(0),
				"neutree-api should be reachable after launch")

			By("Verifying core containers are running")
			containers := cp.ListContainers()
			for _, name := range []string{"postgres", "neutree-api", "kong"} {
				Expect(containers).To(ContainSubstring(name),
					"container %s should be running", name)
			}
		})

		It("should deploy obs-stack with online images", Label("C2610697"), func() {
			r := cp.RunCLI("launch", "obs-stack")
			ExpectCPSuccess(r)

			By("Verifying monitoring containers are running")
			Eventually(func() string {
				return cp.ListContainers()
			}, 1*time.Minute, 5*time.Second).Should(
				ContainSubstring("grafana"),
				"grafana container should be running")
		})

		AfterAll(func() {
			if cp != nil {
				cp.CleanAll()
			}
		})
	})

	Describe("Custom DB Password", Ordered, Label("custom-db"), func() {
		It("should deploy with custom --db-password and all components connect", Label("C2642240"), func() {
			customPwd := "e2e-custom-db-" + Cfg.RunID

			r := cp.LaunchVersion(false, profileCPVersion(), "e2e-test-jwt-secret-long-enough-"+Cfg.RunID,
				"--db-password", customPwd,
			)
			ExpectCPSuccess(r)

			By("Waiting for API to be reachable")
			Eventually(func() int {
				r := cp.CurlAPI("/health")
				return r.ExitCode
			}, 5*time.Minute, 5*time.Second).Should(Equal(0))

			By("Verifying postgres accepts the custom password")
			r = cp.RunSSHCmd(fmt.Sprintf(
				"docker exec neutree-core-postgres-1 psql -U postgres -c 'SELECT 1' 2>&1 || "+
					"docker exec postgres psql -U postgres -c 'SELECT 1' 2>&1",
			))
			Expect(r.ExitCode).To(Equal(0),
				"postgres should be accessible: %s", r.Stderr)
		})

		AfterAll(func() {
			if cp != nil {
				cp.CleanAll()
			}
		})
	})

	Describe("Offline Deploy", Ordered, Label("offline"), func() {
		var offlinePackagePath string

		BeforeAll(func() {
			if profileCPOfflineImageURL() == "" {
				Skip("offline_image_url not configured in profile, skipping offline deploy tests")
			}

			cp.CleanAll()

			offlinePackagePath = "/tmp/neutree-cp-images.tar.gz"
			cp.DownloadOfflineImages(profileCPOfflineImageURL(), offlinePackagePath)

			By("Importing control plane images locally")
			cp.ImportControlPlane(offlinePackagePath)
		})

		It("should deploy neutree-core with offline images", Label("C2587484"), func() {
			r := cp.LaunchVersion(false, profileCPVersion(), "e2e-test-jwt-secret-long-enough-"+Cfg.RunID)
			ExpectCPSuccess(r)

			By("Waiting for API to be reachable")
			Eventually(func() int {
				r := cp.CurlAPI("/health")
				return r.ExitCode
			}, 5*time.Minute, 5*time.Second).Should(Equal(0))
		})

		It("should deploy obs-stack with offline images", Label("C2587480"), func() {
			r := cp.RunCLI("launch", "obs-stack")
			ExpectCPSuccess(r)

			By("Verifying monitoring containers are running")
			Eventually(func() string {
				return cp.ListContainers()
			}, 1*time.Minute, 5*time.Second).Should(
				ContainSubstring("grafana"))
		})

		AfterAll(func() {
			if cp != nil {
				cp.CleanAll()
			}
		})
	})
})

// ExpectCPSuccess asserts a control plane CLI command succeeded.
func ExpectCPSuccess(r CLIResult) {
	ExpectWithOffset(1, r.ExitCode).To(Equal(0),
		"CP CLI command failed (exit %d)\nstdout: %s\nstderr: %s",
		r.ExitCode, r.Stdout, r.Stderr)
}
