package e2e

import (
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Endpoint Lifecycle", Ordered, Label("endpoint", "lifecycle"), func() {
	var clusterName string

	BeforeAll(func() {
		if profileModelName() == "" {
			Skip("Model name not configured in profile, skipping endpoint lifecycle tests")
		}

		clusterName = setupK8sCluster("e2e-ep-lc-")

		By("Setting up model registry")
		SetupModelRegistry()
	})

	AfterAll(func() {
		TeardownModelRegistry()
		teardownCluster(clusterName)
	})

	// --- Status Check ---

	Describe("Status Check", Label("status"), func() {

		It("should delete a running endpoint", Label("C2612951", "C2612923"), func() {
			epName := "e2e-ep-lc-delrun-" + Cfg.RunID
			DeferCleanup(func() { deleteEndpoint(epName) })

			yamlPath := applyEndpoint(epName, clusterName)
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)
			deleteEndpoint(epName)

			r := RunCLI("get", "endpoint", epName, "-w", profileWorkspace(), "-o", "json")
			ExpectFailed(r)
		})

		It("should delete a failed endpoint", Label("C2612952"), func() {
			epName := "e2e-ep-lc-delfail-" + Cfg.RunID
			DeferCleanup(func() { deleteEndpoint(epName) })

			yamlPath := applyEndpoint(epName, clusterName,
				withModel("non-existent-model-"+Cfg.RunID, "v0.0.0"), withoutForceUpdate())
			defer os.Remove(yamlPath)

			waitEndpointFailed(epName)
			deleteEndpoint(epName)

			r := RunCLI("get", "endpoint", epName, "-w", profileWorkspace(), "-o", "json")
			ExpectFailed(r)
		})

		It("should verify resources are cleaned up after deletion", Label("C2613295"), func() {
			epName := "e2e-ep-lc-cleanup-" + Cfg.RunID
			DeferCleanup(func() { deleteEndpoint(epName) })

			yamlPath := applyEndpoint(epName, clusterName)
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)
			deleteEndpoint(epName)

			r := RunCLI("get", "endpoint", "-w", profileWorkspace())
			ExpectSuccess(r)
			Expect(r.Stdout).NotTo(ContainSubstring(epName))
		})

		It("should recreate endpoint with same name after deletion", Label("C2644061"), func() {
			epName := "e2e-ep-lc-recreate-" + Cfg.RunID
			DeferCleanup(func() { deleteEndpoint(epName) })

			yamlPath := applyEndpoint(epName, clusterName)
			os.Remove(yamlPath)
			waitEndpointRunning(epName)

			r := RunCLI("delete", "endpoint", epName, "-w", profileWorkspace())
			ExpectSuccess(r)
			r = RunCLI("wait", "endpoint", epName,
				"-w", profileWorkspace(),
				"--for", "delete",
				"--timeout", "5m",
			)
			ExpectSuccess(r)

			yamlPath = applyEndpoint(epName, clusterName)
			defer os.Remove(yamlPath)
			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Running"))
		})

		// NEU-421: delete a running endpoint without --force after its model
		// registry has been removed. Pre-fix: stuck in DELETING. Post-fix:
		// converges to Deleted via deployer's last-applied snapshot.
		It("should reach Deleted when running endpoint is deleted after model removed without --force (NEU-421)", Label("C2649426"), func() {
			epName := "e2e-ep-lc-del-after-del-" + Cfg.RunID
			DeferCleanup(func() {
				// Best-effort cleanup; SetupModelRegistry restores for siblings.
				RunCLI("delete", "endpoint", epName, "-w", profileWorkspace(), "--force", "--ignore-not-found")
				SetupModelRegistry()
			})

			By("Creating endpoint and waiting for Running")
			yamlPath := applyEndpoint(epName, clusterName)
			defer os.Remove(yamlPath)
			waitEndpointRunning(epName)

			By("Removing the model registry the endpoint references")
			TeardownModelRegistry()

			By("Deleting endpoint without --force")
			r := RunCLI("delete", "endpoint", epName, "-w", profileWorkspace(), "--ignore-not-found")
			ExpectSuccess(r)

			By("Endpoint converges to deleted (does not get stuck in DELETING)")
			r = RunCLI("wait", "endpoint", epName,
				"-w", profileWorkspace(),
				"--for", "delete",
				"--timeout", profileEndpointTimeout(),
			)
			ExpectSuccess(r)
		})
	})

	// --- Error Handling ---

	Describe("Error Handling", Label("error"), func() {

		// NEU-421 R4: contract change — config errors no longer flip to Failed.
		// The orchestrator surfaces Deploying with a specific reason via
		// status.errorMessage so the operator can act on the cause. Failed
		// remains reserved for observed-Failed states (CrashLoopBackOff etc.).
		It("should show Deploying with errorMessage when model does not exist", Label("C2612944"), func() {
			epName := "e2e-ep-lc-fail-" + Cfg.RunID
			DeferCleanup(func() { deleteEndpoint(epName) })

			yamlPath := applyEndpoint(epName, clusterName,
				withModel("non-existent-model-"+Cfg.RunID, "v0.0.0"), withoutForceUpdate())
			defer os.Remove(yamlPath)

			waitEndpointDeployingWithError(epName, "model")
		})

		It("should show Deploying with errorMessage when model version does not exist", Label("C2613501"), func() {
			epName := "e2e-ep-lc-badver-" + Cfg.RunID
			DeferCleanup(func() { deleteEndpoint(epName) })

			yamlPath := applyEndpoint(epName, clusterName,
				withModel(profileModelName(), "v99.99.99-nonexistent"))
			defer os.Remove(yamlPath)

			waitEndpointDeployingWithError(epName, "version")
		})

		// TODO: C2613502 needs to run on SSH cluster — K8s pod stays Pending instead of Failed
		It("should not reach Running when resources exceed capacity", Label("C2613502"), func() {
			epName := "e2e-ep-lc-bigres-" + Cfg.RunID
			DeferCleanup(func() { deleteEndpoint(epName) })

			yamlPath := applyEndpoint(epName, clusterName, withGPU("100"))
			defer os.Remove(yamlPath)

			Consistently(func() string {
				r := RunCLI("get", "endpoint", epName, "-w", profileWorkspace(), "-o", "json")
				if r.ExitCode != 0 {
					return ""
				}

				ep := parseEndpointJSON(r.Stdout)
				if ep.Status != nil {
					return string(ep.Status.Phase)
				}

				return ""
			}, 2*time.Minute, 10*time.Second).ShouldNot(Equal("Running"),
				"endpoint with 100 GPUs should not reach Running")

			By("Verifying endpoint eventually reaches Failed")
			waitEndpointFailed(epName)
			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Failed"))
		})

		It("should show Deploying with errorMessage when using unsupported engine", Label("C2612936"), func() {
			epName := "e2e-ep-lc-badeng-" + Cfg.RunID
			DeferCleanup(func() { deleteEndpoint(epName) })

			yamlPath := applyEndpoint(epName, clusterName,
				withEngine("nonexistent-engine-"+Cfg.RunID, "v0.0.1"))
			defer os.Remove(yamlPath)

			waitEndpointDeployingWithError(epName, "engine")
		})

		It("should show Deploying with errorMessage when no matching accelerator product", Label("C2613503"), func() {
			epName := "e2e-ep-lc-badacc-" + Cfg.RunID
			DeferCleanup(func() { deleteEndpoint(epName) })

			accType, _ := getClusterAccelerator(clusterName)

			yamlPath := applyEndpoint(epName, clusterName,
				withAccelerator(accType, "NONEXISTENT-GPU-9999"))
			defer os.Remove(yamlPath)

			waitEndpointDeployingWithError(epName, "accelerator")
		})

		It("should show Failed when deployment is unhealthy", Label("C2642243"), func() {
			epName := "e2e-ep-lc-unhealth-" + Cfg.RunID
			DeferCleanup(func() { deleteEndpoint(epName) })

			yamlPath := applyEndpoint(epName, clusterName, withGPU("99"))
			defer os.Remove(yamlPath)

			waitEndpointFailed(epName)
		})

		It("should reject accelerator with non-string values", Label("C2642283"), func() {
			accType, _ := getClusterAccelerator(clusterName)

			invalidProducts := []struct {
				label string
				value string
			}{
				{"nested object", `{"nested": "object"}`},
				{"array value", `["GPU-A", "GPU-B"]`},
				{"numeric value", `12345`},
			}

			for _, tc := range invalidProducts {
				By("Testing " + tc.label)
				epName := "e2e-ep-lc-badacc-type-" + Cfg.RunID

				yamlPath, _ := renderEndpoint(epName, clusterName,
					withAccelerator(accType, tc.value))
				r := RunCLI("apply", "-f", yamlPath)
				os.Remove(yamlPath)
				ExpectFailed(r)
			}
		})
	})

	// --- Pause / NEU-421 ---

	Describe("Pause", Label("pause"), func() {

		It("should reach Paused when applied with replicas=0 and valid model", Label("C2649423"), func() {
			epName := "e2e-ep-lc-pause-ok-" + Cfg.RunID
			DeferCleanup(func() { deleteEndpoint(epName) })

			yamlPath := applyEndpoint(epName, clusterName, withReplicas(0))
			defer os.Remove(yamlPath)

			waitEndpointPaused(epName)
			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Paused"))
		})

		It("should reach Paused when applied with replicas=0 and non-existent model (NEU-421)", Label("C2649424"), func() {
			epName := "e2e-ep-lc-pause-nomodel-" + Cfg.RunID
			DeferCleanup(func() { deleteEndpoint(epName) })

			yamlPath := applyEndpoint(epName, clusterName,
				withReplicas(0),
				withModel("non-existent-model-"+Cfg.RunID, "v0.0.0"),
				withoutForceUpdate())
			defer os.Remove(yamlPath)

			waitEndpointPaused(epName)
			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Paused"))
		})

		It("should reach Paused when running endpoint is paused after model deleted (NEU-421)", Label("C2649425"), func() {
			epName := "e2e-ep-lc-pause-after-del-" + Cfg.RunID
			DeferCleanup(func() {
				deleteEndpoint(epName)
				// Restore registry for sibling cases.
				SetupModelRegistry()
			})

			By("Creating endpoint with replicas=1 and waiting for Running")
			yamlPath := applyEndpoint(epName, clusterName)
			defer os.Remove(yamlPath)
			waitEndpointRunning(epName)

			By("Removing the model registry the endpoint references")
			TeardownModelRegistry()

			By("Re-applying endpoint with replicas=0 (pause)")
			yamlPath2 := applyEndpoint(epName, clusterName, withReplicas(0), withoutForceUpdate())
			defer os.Remove(yamlPath2)

			By("Endpoint converges to Paused even though model registry is gone")
			waitEndpointPaused(epName)
			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Paused"))
		})
	})

})
