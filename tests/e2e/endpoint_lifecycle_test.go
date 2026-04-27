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
	})

	// --- Error Handling ---

	Describe("Error Handling", Label("error"), func() {

		It("should show Failed when model does not exist", Label("C2612944"), func() {
			epName := "e2e-ep-lc-fail-" + Cfg.RunID
			DeferCleanup(func() { deleteEndpoint(epName) })

			yamlPath := applyEndpoint(epName, clusterName,
				withModel("non-existent-model-"+Cfg.RunID, "v0.0.0"), withoutForceUpdate())
			defer os.Remove(yamlPath)

			waitEndpointFailed(epName)
			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Failed"))
		})

		It("should show Failed when model version does not exist", Label("C2613501"), func() {
			epName := "e2e-ep-lc-badver-" + Cfg.RunID
			DeferCleanup(func() { deleteEndpoint(epName) })

			yamlPath := applyEndpoint(epName, clusterName,
				withModel(profileModelName(), "v99.99.99-nonexistent"))
			defer os.Remove(yamlPath)

			waitEndpointFailed(epName)
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

		It("should show Failed when using unsupported engine", Label("C2612936"), func() {
			epName := "e2e-ep-lc-badeng-" + Cfg.RunID
			DeferCleanup(func() { deleteEndpoint(epName) })

			yamlPath := applyEndpoint(epName, clusterName,
				withEngine("nonexistent-engine-"+Cfg.RunID, "v0.0.1"))
			defer os.Remove(yamlPath)

			waitEndpointFailed(epName)
		})

		It("should show Failed when no matching accelerator product", Label("C2613503"), func() {
			epName := "e2e-ep-lc-badacc-" + Cfg.RunID
			DeferCleanup(func() { deleteEndpoint(epName) })

			accType, _ := getClusterAccelerator(clusterName)

			yamlPath := applyEndpoint(epName, clusterName,
				withAccelerator(accType, "NONEXISTENT-GPU-9999"))
			defer os.Remove(yamlPath)

			waitEndpointFailed(epName)
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

})
