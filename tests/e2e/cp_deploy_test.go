package e2e

import (
	"fmt"
	"time"

	"github.com/Masterminds/semver/v3"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Control Plane Deploy", Ordered, Label("control-plane", "deploy"), func() {
	var cph *CPHelper

	BeforeAll(func() {
		cfg := requireCPProfile()
		cph = NewCPHelper(cfg)

		By("Deploying CLI binary to machine")
		cph.DeployCLIBinary()

		By("Cleaning machine to pristine state")
		cph.CleanAll()
	})

	AfterAll(func() {
		if cph != nil {
			cph.CleanAll()
		}
	})

	// --- Parameter validation (no install needed) ---

	Describe("Parameter Validation", Label("validation"), func() {
		It("should reject launch without --jwt-secret", Label("C2642239"), func() {
			r := cph.RunCLI("launch", "neutree-core",
				"--version", profileCPVersion(),
			)
			Expect(r.ExitCode).NotTo(Equal(0),
				"launch without jwt-secret should fail")
			Expect(r.Stderr+r.Stdout).To(
				ContainSubstring("jwt-secret"),
				"error should mention jwt-secret")
		})

		It("should reject incompatible target version before mutating compose", Label("C2707965"), func() {
			incompatibleVersion, err := nextMinorCPVersion(profileCPVersion())
			Expect(err).NotTo(HaveOccurred())

			sentinel := "neu-462-sentinel-compose"
			r := cph.RunCmd(fmt.Sprintf("mkdir -p %s/neutree-core && printf %%s %q > %s/neutree-core/docker-compose.yml",
				cph.composeDir, sentinel, cph.composeDir))
			ExpectSuccess(r)

			r = cph.RunCLI("launch", "neutree-core",
				"--jwt-secret", "e2e-test-jwt-secret-long-enough-"+Cfg.RunID,
				"--dry-run",
				"--version", incompatibleVersion,
			)
			Expect(r.ExitCode).NotTo(Equal(0),
				"launch with incompatible target version should fail")
			Expect(r.Stderr+r.Stdout).To(
				ContainSubstring("not compatible"),
				"error should mention version compatibility")

			r = cph.RunCmd("cat " + cph.composeDir + "/neutree-core/docker-compose.yml")
			ExpectSuccess(r)
			Expect(r.Stdout).To(Equal(sentinel),
				"incompatible launch should not overwrite existing compose")
		})
	})

	// --- Online deploy: pull from default registry (no mirror) ---

	Describe("Online Deploy", Ordered, Label("online"), func() {
		BeforeAll(func() {
			cph.CleanAll()

			By("Launching obs-stack first (metrics endpoint must be ready)")
			r := cph.RunCLI("launch", "obs-stack")
			ExpectSuccess(r)

			By("Launching neutree-core with online images")
			r = cph.RunCLI("launch", "neutree-core",
				"--jwt-secret", "e2e-test-jwt-secret-long-enough-"+Cfg.RunID,
				"--version", profileCPVersion(),
				"--admin-password", profile.Auth.Password,
				"--metrics-remote-write-url", cph.MetricsRemoteWriteURL(),
			)
			ExpectSuccess(r)

			Eventually(cph.CanLogin, 5*time.Minute, 5*time.Second).Should(BeTrue(),
				"admin login should succeed after launch")
		})

		AfterAll(func() {
			if cph != nil {
				cph.CleanAll()
			}
		})

		It("should have all neutree-core containers healthy", Label("C2610706"), func() {
			cph.VerifyDeployed(cph.ParseCompose("neutree-core"))
		})

		It("should have all obs-stack containers healthy", Label("C2610697"), func() {
			cph.VerifyDeployed(cph.ParseCompose("obs-stack"))
		})
	})

	// --- Mirror registry deploy: custom registry + custom DB (multiple checks) ---

	Describe("Custom Deploy", Ordered, Label("custom-deploy"), func() {
		BeforeAll(func() {
			if profileCPMirrorRegistry() == "" {
				Skip("control_plane.mirror_registry not configured")
			}

			cph.CleanAll()

			customPwd := "e2e-custom-db-" + Cfg.RunID

			By("Launching obs-stack first")
			r := cph.RunCLI(append([]string{"launch", "obs-stack"}, mirrorRegistryArgs()...)...)
			ExpectSuccess(r)

			By("Launching neutree-core with mirror registry + custom db-password")
			args := append([]string{"launch", "neutree-core",
				"--jwt-secret", "e2e-test-jwt-secret-long-enough-" + Cfg.RunID,
				"--version", profileCPVersion(),
				"--admin-password", profile.Auth.Password,
				"--db-password", customPwd,
				"--metrics-remote-write-url", cph.MetricsRemoteWriteURL(),
			}, mirrorRegistryArgs()...)
			r = cph.RunCLI(args...)
			ExpectSuccess(r)

			Eventually(cph.CanLogin, 5*time.Minute, 5*time.Second).Should(BeTrue(),
				"admin login should succeed after launch")
		})

		AfterAll(func() {
			if cph != nil {
				cph.CleanAll()
			}
		})

		It("should have all neutree-core containers healthy", Label("C2610706"), func() {
			cph.VerifyDeployed(cph.ParseCompose("neutree-core"))
		})

		It("should have all obs-stack containers healthy", Label("C2610697"), func() {
			cph.VerifyDeployed(cph.ParseCompose("obs-stack"))
		})

		It("should use mirror registry images in compose file", Label("C2642203"), func() {
			r := cph.RunCmd("cat " + profileCPComposeDir() + "/neutree-core/docker-compose.yml")
			Expect(r.ExitCode).To(Equal(0), "should read compose file")

			Expect(r.Stdout).To(ContainSubstring(profileCPMirrorRegistry()),
				"compose file should reference mirror registry")
			if rp := profileCPRegistryProject(); rp != "" {
				Expect(r.Stdout).To(ContainSubstring(rp),
					"compose file should reference registry project")
			}
		})

		It("should connect with custom db-password", Label("C2642240"), func() {
			jwt, err := loginTestUser(cph.APIURL(), profile.Auth.Email, profile.Auth.Password)
			Expect(err).NotTo(HaveOccurred())
			Expect(jwt).NotTo(BeEmpty(),
				"admin login should succeed, proving all components connect with custom db-password")
		})
	})

	// --- Offline deploy (separate: import images first) ---

	Describe("Offline Deploy", Ordered, Label("offline"), func() {
		BeforeAll(func() {
			if profileCPOfflineImageURL() == "" {
				Skip("offline_image_url not configured in profile")
			}

			cph.RemoveImages()
			cph.CleanAll()

			offlinePackagePath := "/tmp/neutree-cp-images.tar.gz"
			cph.DownloadOfflineImages(profileCPOfflineImageURL(), offlinePackagePath)

			By("Importing control plane images locally")
			cph.ImportControlPlane(offlinePackagePath)

			By("Launching obs-stack with offline images")
			r := cph.RunCLI("launch", "obs-stack")
			ExpectSuccess(r)

			By("Launching neutree-core with offline images")
			r = cph.RunCLI("launch", "neutree-core",
				"--jwt-secret", "e2e-test-jwt-secret-long-enough-"+Cfg.RunID,
				"--version", profileCPVersion(),
				"--admin-password", profile.Auth.Password,
				"--metrics-remote-write-url", cph.MetricsRemoteWriteURL(),
			)
			ExpectSuccess(r)

			Eventually(cph.CanLogin, 5*time.Minute, 5*time.Second).Should(BeTrue(),
				"admin login should succeed after launch")
		})

		AfterAll(func() {
			if cph != nil {
				cph.CleanAll()
			}
		})

		It("should have all neutree-core containers healthy", Label("C2587484"), func() {
			cph.VerifyDeployed(cph.ParseCompose("neutree-core"))
		})

		It("should have all obs-stack containers healthy", Label("C2587480"), func() {
			cph.VerifyDeployed(cph.ParseCompose("obs-stack"))
		})
	})
})

func nextMinorCPVersion(version string) (string, error) {
	v, err := semver.NewVersion(version)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("v%d.%d.0", v.Major(), v.Minor()+1), nil
}
