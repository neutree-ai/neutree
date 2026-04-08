package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

var _ = Describe("SSH Endpoint", Ordered, Label("endpoint", "ssh"), func() {
	var clusterName string

	BeforeAll(func() {
		if profileModelName() == "" {
			Skip("Model name not configured in profile, skipping SSH endpoint tests")
		}

		clusterName = setupSSHCluster("e2e-ep-ssh-")

		By("Setting up model registry")
		SetupModelRegistry()
	})

	AfterAll(func() {
		TeardownModelRegistry()
		teardownCluster(clusterName)
	})

	// --- Chat Inference ---

	Describe("Chat Inference", Ordered, Label("inference", "chat"), func() {
		var epName string

		BeforeAll(func() {
			epName = "e2e-ep-ssh-chat-" + Cfg.RunID
		})

		AfterAll(func() {
			if epName != "" {
				deleteEndpoint(epName)
			}
		})

		It("should deploy with engine container and reach Running", Label("C2613491"), func() {
			yamlPath := applyEndpointOnCluster(epName, clusterName, profileEngineVersion())
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Running"))
			Expect(ep.Spec.Engine.Version).To(Equal(profileEngineVersion()))
			Expect(ep.Status.ServiceURL).NotTo(BeEmpty())
		})

		It("should serve inference requests", Label("C2642267"), func() {
			ep := getEndpoint(epName)
			code, body := inferChat(ep.Status.ServiceURL, "Hello")
			Expect(code).To(Equal(http.StatusOK), "inference failed: %s", body)
			Expect(body).To(ContainSubstring("choices"))
		})

		It("should return error for wrong model name", Label("inference-error"), func() {
			ep := getEndpoint(epName)

			By("Sending request with non-existent model name")
			code, body := doInferenceRequest(ep.Status.ServiceURL, "/v1/chat/completions", map[string]any{
				"model": "non-existent-model-name",
				"messages": []map[string]string{
					{"role": "user", "content": "hello"},
				},
				"max_tokens": 8,
			})

			Expect(code).To(BeElementOf(http.StatusBadRequest, http.StatusNotFound),
				"request with wrong model name should return 400 or 404, got %d, body: %s", code, body)
		})

		It("should route requests via ExternalEndpoint with endpoint_ref", Label("C2642211"), func() {
			eeName := "e2e-ee-ref-" + Cfg.RunID

			By("Creating ExternalEndpoint with endpoint_ref to internal endpoint")
			eeYAML := fmt.Sprintf(`apiVersion: v1
kind: ExternalEndpoint
metadata:
  name: %s
  workspace: %s
spec:
  upstreams:
    - endpoint_ref: "%s"
      model_mapping:
        test-model: %s
`, eeName, profileWorkspace(), epName, profileModelName())

			tmpFile, err := os.CreateTemp("", "e2e-ee-ref-*.yaml")
			Expect(err).NotTo(HaveOccurred())
			_, err = tmpFile.WriteString(eeYAML)
			Expect(err).NotTo(HaveOccurred())
			tmpFile.Close()
			defer os.Remove(tmpFile.Name())

			r := RunCLI("apply", "-f", tmpFile.Name())
			ExpectSuccess(r)

			DeferCleanup(func() {
				RunCLI("delete", "ExternalEndpoint", eeName,
					"-w", profileWorkspace(), "--force", "--ignore-not-found")
			})

			By("Waiting for ExternalEndpoint to reach Running")
			r = RunCLI("wait", "ExternalEndpoint", eeName,
				"-w", profileWorkspace(),
				"--for", "jsonpath=.status.phase=Running",
				"--timeout", "2m",
			)
			ExpectSuccess(r)

			By("Getting ExternalEndpoint service URL")
			r = RunCLI("get", "ExternalEndpoint", eeName,
				"-w", profileWorkspace(), "-o", "json")
			ExpectSuccess(r)

			var ee map[string]any
			Expect(json.Unmarshal([]byte(r.Stdout), &ee)).To(Succeed())
			status := ee["status"].(map[string]any)
			eeServiceURL := status["service_url"].(string)
			Expect(eeServiceURL).NotTo(BeEmpty())

			By("Waiting for Kong route to become reachable")
			client := &http.Client{Timeout: 30 * time.Second}
			Eventually(func() int {
				reqBody := `{"model":"test-model","messages":[{"role":"user","content":"hi"}],"max_tokens":8}`
				req, _ := http.NewRequest(http.MethodPost,
					strings.TrimRight(eeServiceURL, "/")+"/v1/chat/completions",
					strings.NewReader(reqBody))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", bearerAPIKey())
				resp, err := client.Do(req)
				if err != nil {
					return 0
				}
				defer resp.Body.Close()
				return resp.StatusCode
			}, 30*time.Second, 2*time.Second).Should(Equal(http.StatusOK))

			By("Sending inference request via ExternalEndpoint")
			reqBody := `{"model":"test-model","messages":[{"role":"user","content":"hello endpoint_ref"}],"max_tokens":16}`
			req, err := http.NewRequest(http.MethodPost,
				strings.TrimRight(eeServiceURL, "/")+"/v1/chat/completions",
				strings.NewReader(reqBody))
			Expect(err).NotTo(HaveOccurred())
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", bearerAPIKey())

			resp, err := client.Do(req)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()

			body, err := io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())

			Expect(resp.StatusCode).To(Equal(http.StatusOK),
				"inference via endpoint_ref should return 200, got: %s", string(body))
			Expect(string(body)).To(ContainSubstring("choices"),
				"response should contain inference result")
		})
	})

	// --- Multi-Version Isolation ---

	Describe("Multi-Version Isolation", Ordered, Label("inference", "multi-version"), func() {
		var epNameA, epNameB string

		BeforeAll(func() {
			epNameA = "e2e-ep-ssh-va-" + Cfg.RunID
			epNameB = "e2e-ep-ssh-vb-" + Cfg.RunID
		})

		AfterAll(func() {
			deleteEndpoint(epNameA)
			deleteEndpoint(epNameB)
		})

		It("should run two endpoints with different engine versions", Label("C2642251"), func() {
			yamlA := applyEndpointOnCluster(epNameA, clusterName, profileEngineOldVersion())
			defer os.Remove(yamlA)
			yamlB := applyEndpointOnCluster(epNameB, clusterName, profileEngineVersion())
			defer os.Remove(yamlB)

			waitEndpointRunning(epNameA)
			waitEndpointRunning(epNameB)

			epA := getEndpoint(epNameA)
			epB := getEndpoint(epNameB)
			Expect(epA.Status.Phase).To(BeEquivalentTo("Running"))
			Expect(epB.Status.Phase).To(BeEquivalentTo("Running"))
			Expect(epA.Spec.Engine.Version).To(Equal(profileEngineOldVersion()))
			Expect(epB.Spec.Engine.Version).To(Equal(profileEngineVersion()))
		})

		It("should serve inference from both endpoints", func() {
			epA := getEndpoint(epNameA)
			epB := getEndpoint(epNameB)

			codeA, bodyA := inferChat(epA.Status.ServiceURL, "Hello")
			Expect(codeA).To(Equal(http.StatusOK), "inference on ep-A failed: %s", bodyA)

			codeB, bodyB := inferChat(epB.Status.ServiceURL, "Hello")
			Expect(codeB).To(Equal(http.StatusOK), "inference on ep-B failed: %s", bodyB)
		})

		It("should not affect other endpoint when deleting one", Label("C2642252"), func() {
			// Delete endpoint A (old version)
			deleteEndpoint(epNameA)

			// Verify endpoint B (new version) still works
			epB := getEndpoint(epNameB)
			Expect(epB.Status.Phase).To(BeEquivalentTo("Running"))

			codeB, bodyB := inferChat(epB.Status.ServiceURL, "Hello after delete")
			Expect(codeB).To(Equal(http.StatusOK), "inference on ep-B after deleting ep-A failed: %s", bodyB)
		})
	})

	// --- vLLM v0.17.1 + v0.11.2 Coexistence ---

	Describe("vLLM v0.17.1 Coexistence", Ordered, Label("inference", "v0171-coexist"), func() {
		var epOld, epNew string

		BeforeAll(func() {
			requireEngineVersion("vllm", "v0.17.1")
			epOld = "e2e-ep-ssh-v0112-" + Cfg.RunID
			epNew = "e2e-ep-ssh-v0171-" + Cfg.RunID
		})

		AfterAll(func() {
			deleteEndpoint(epOld)
			deleteEndpoint(epNew)
		})

		It("should run v0.11.2 and v0.17.1 endpoints on same cluster", Label("C2644374"), func() {
			yamlOld := applyEndpointOnCluster(epOld, clusterName, "v0.11.2")
			defer os.Remove(yamlOld)
			waitEndpointRunning(epOld)

			yamlNew := applyEndpointOnCluster(epNew, clusterName, "v0.17.1")
			defer os.Remove(yamlNew)
			waitEndpointRunning(epNew)

			// Both should be Running
			epOldObj := getEndpoint(epOld)
			epNewObj := getEndpoint(epNew)
			Expect(epOldObj.Status.Phase).To(BeEquivalentTo("Running"))
			Expect(epNewObj.Status.Phase).To(BeEquivalentTo("Running"))

			// Both should serve inference
			codeOld, bodyOld := inferChat(epOldObj.Status.ServiceURL, "Hello v0.11.2")
			Expect(codeOld).To(Equal(http.StatusOK), "v0.11.2 inference failed: %s", bodyOld)

			codeNew, bodyNew := inferChat(epNewObj.Status.ServiceURL, "Hello v0.17.1")
			Expect(codeNew).To(Equal(http.StatusOK), "v0.17.1 inference failed: %s", bodyNew)
		})
	})

	// --- Tensor Parallel (TP=2) ---

	Describe("Tensor Parallel TP=2", Ordered, Label("inference", "tp2"), func() {
		var epName string

		BeforeAll(func() {
			epName = "e2e-ep-ssh-tp2-" + Cfg.RunID
		})

		AfterAll(func() {
			if epName != "" {
				deleteEndpoint(epName)
			}
		})

		It("should deploy with tp=2 (gpu=2) and reach Running", Label("C2613759", "C2642248"), func() {
			yamlPath := applyEndpointOnCluster(epName, clusterName, profileEngineVersion())
			defer os.Remove(yamlPath)

			// Patch gpu=2 and add tensor_parallel_size=2
			content, err := os.ReadFile(yamlPath)
			Expect(err).NotTo(HaveOccurred())

			patched := strings.Replace(string(content), `gpu: "1"`, `gpu: "2"`, 1)
			patched = strings.Replace(patched, "engine_args:", "engine_args:\n      tensor_parallel_size: 2", 1)
			Expect(os.WriteFile(yamlPath, []byte(patched), 0o644)).To(Succeed())

			r := RunCLI("apply", "-f", yamlPath, "--force-update")
			ExpectSuccess(r)

			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Running"))
			Expect(ep.Status.ServiceURL).NotTo(BeEmpty())

			By("Verifying tensor_parallel_size=2 in Ray Serve config")
			c := getClusterFullJSON(clusterName)
			rayH := NewRayHelper(c.Status.DashboardURL)
			apps, err := rayH.GetServeApplications()
			Expect(err).NotTo(HaveOccurred())

			found := false
			for _, appStatus := range apps.Applications {
				if appStatus.DeployedAppConfig == nil || appStatus.DeployedAppConfig.Args == nil {
					continue
				}
				engineArgs, ok := appStatus.DeployedAppConfig.Args["engine_args"].(map[string]interface{})
				if !ok {
					continue
				}
				if tp, ok := engineArgs["tensor_parallel_size"]; ok {
					// JSON numbers unmarshal as float64
					Expect(tp).To(BeNumerically("==", 2),
						"tensor_parallel_size should be 2 (user-specified value)")
					found = true

					break
				}
			}
			Expect(found).To(BeTrue(), "should find tensor_parallel_size in Ray Serve engine_args")
		})

		It("should serve inference with tp=2", func() {
			ep := getEndpoint(epName)
			code, body := inferChat(ep.Status.ServiceURL, "Hello with TP=2")
			Expect(code).To(Equal(http.StatusOK), "inference with tp=2 failed: %s", body)
			Expect(body).To(ContainSubstring("choices"))
		})
	})

	// --- Auto Tensor Parallel ---

	Describe("Auto Tensor Parallel", Ordered, Label("inference", "auto-tp"), func() {
		var epName string

		BeforeAll(func() {
			epName = "e2e-ep-ssh-autotp-" + Cfg.RunID
		})

		AfterAll(func() {
			if epName != "" {
				deleteEndpoint(epName)
			}
		})

		It("should auto-set tensor_parallel_size to GPU count when not specified", Label("C2642247"), func() {
			// Deploy with gpu=2 but NO tensor_parallel_size
			yamlPath := applyEndpointOnCluster(epName, clusterName, profileEngineVersion())
			defer os.Remove(yamlPath)

			// Patch gpu=2 without adding tensor_parallel_size
			content, err := os.ReadFile(yamlPath)
			Expect(err).NotTo(HaveOccurred())
			patched := strings.Replace(string(content), `gpu: "1"`, `gpu: "2"`, 1)
			Expect(os.WriteFile(yamlPath, []byte(patched), 0o644)).To(Succeed())

			r := RunCLI("apply", "-f", yamlPath, "--force-update")
			ExpectSuccess(r)

			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Running"))

			// Verify inference works
			code, body := inferChat(ep.Status.ServiceURL, "Hello auto-TP")
			Expect(code).To(Equal(http.StatusOK), "inference with auto-TP failed: %s", body)

			By("Verifying tensor_parallel_size auto-set to GPU count (2) in Ray Serve config")
			c := getClusterFullJSON(clusterName)
			rayH := NewRayHelper(c.Status.DashboardURL)
			apps, err := rayH.GetServeApplications()
			Expect(err).NotTo(HaveOccurred())

			found := false
			for _, appStatus := range apps.Applications {
				if appStatus.DeployedAppConfig == nil || appStatus.DeployedAppConfig.Args == nil {
					continue
				}
				engineArgs, ok := appStatus.DeployedAppConfig.Args["engine_args"].(map[string]interface{})
				if !ok {
					continue
				}
				if tp, ok := engineArgs["tensor_parallel_size"]; ok {
					Expect(tp).To(BeNumerically("==", 2),
						"tensor_parallel_size should be auto-set to GPU count (2)")
					found = true

					break
				}
			}
			Expect(found).To(BeTrue(), "should find tensor_parallel_size in Ray Serve engine_args")
		})
	})

	// --- Embedding Inference ---

	Describe("Embedding Inference", Ordered, Label("inference", "embedding"), func() {
		var epName string

		BeforeAll(func() {
			if profileEmbeddingModelName() == "" {
				Skip("embedding_model.name not configured")
			}
			epName = "e2e-ep-ssh-embed-" + Cfg.RunID
		})

		AfterAll(func() {
			if epName != "" {
				deleteEndpoint(epName)
			}
		})

		It("should deploy and serve embedding requests", func() {
			yamlPath := applyEndpointWithTask(epName, clusterName, profileEngineVersion(),
				profileEmbeddingModelName(), profileEmbeddingModelVersion(), "text-embedding",
				"")
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			code, body := inferEmbedding(ep.Status.ServiceURL, profileEmbeddingModelName(), "Hello world")
			Expect(code).To(Equal(http.StatusOK), "embedding inference failed: %s", body)

			var resp map[string]any
			Expect(json.Unmarshal([]byte(body), &resp)).To(Succeed())
			Expect(resp).To(HaveKey("data"))
		})
	})

	// --- Rerank Inference ---

	Describe("Rerank Inference", Ordered, Label("inference", "rerank"), func() {
		var epName string

		BeforeAll(func() {
			if profileRerankModelName() == "" {
				Skip("rerank_model.name not configured")
			}
			epName = "e2e-ep-ssh-rerank-" + Cfg.RunID
		})

		AfterAll(func() {
			if epName != "" {
				deleteEndpoint(epName)
			}
		})

		It("should deploy and serve rerank requests", func() {
			yamlPath := applyEndpointWithTask(epName, clusterName, profileEngineVersion(),
				profileRerankModelName(), profileRerankModelVersion(), "text-rerank",
				"")
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			code, body := inferRerank(ep.Status.ServiceURL, profileRerankModelName(),
				"What is the capital of France?", []string{"Paris is the capital of France.", "Berlin is in Germany."})
			Expect(code).To(Equal(http.StatusOK), "rerank inference failed: %s", body)

			var resp map[string]any
			Expect(json.Unmarshal([]byte(body), &resp)).To(Succeed())
			Expect(resp).To(HaveKey("results"))
		})
	})

	// --- Status & Delete ---

	Describe("Status & Delete", Label("status"), func() {

		It("should show Pending on creation", Label("C2613490"), func() {
			epName := "e2e-ep-ssh-pend-" + Cfg.RunID
			DeferCleanup(func() { deleteEndpoint(epName) })

			yamlPath := applyEndpointOnCluster(epName, clusterName, profileEngineVersion())
			defer os.Remove(yamlPath)

			r := RunCLI("get", "endpoint", epName, "-w", profileWorkspace(), "-o", "json")
			ExpectSuccess(r)
			ep := parseEndpointJSON(r.Stdout)

			phase := v1.EndpointPhase("")
			if ep.Status != nil {
				phase = ep.Status.Phase
			}

			if phase == v1.EndpointPhase("Running") {
				GinkgoWriter.Printf("WARNING: endpoint already Running, Pending phase was too fast to capture\n")
			} else {
				Expect(phase).To(BeElementOf(
					v1.EndpointPhase(""), v1.EndpointPhase("Pending"), v1.EndpointPhase("Deploying")),
					"endpoint should be in empty/Pending/Deploying, got %s", phase)
			}
		})

		It("should show Failed when model does not exist", Label("C2612944"), func() {
			epName := "e2e-ep-ssh-fail-" + Cfg.RunID
			DeferCleanup(func() { deleteEndpoint(epName) })

			yamlPath := applyFailingEndpoint(epName, clusterName)
			defer os.Remove(yamlPath)

			waitEndpointFailed(epName)
			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Failed"))
		})

		It("should show Failed when model version does not exist", Label("C2613501"), func() {
			epName := "e2e-ep-ssh-badver-" + Cfg.RunID
			DeferCleanup(func() { deleteEndpoint(epName) })

			accType, accProduct := getClusterAccelerator(clusterName)

			defaults := map[string]string{
				"E2E_ENDPOINT_NAME":       epName,
				"E2E_WORKSPACE":           profileWorkspace(),
				"E2E_CLUSTER_NAME":        clusterName,
				"E2E_ENGINE_NAME":         profileEngineName(),
				"E2E_ENGINE_VERSION":      profileEngineVersion(),
				"E2E_MODEL_REGISTRY":      testRegistry(),
				"E2E_MODEL_NAME":          profileModelName(),
				"E2E_MODEL_VERSION":       "v99.99.99-nonexistent",
				"E2E_MODEL_TASK":          profileModelTask(),
				"E2E_ACCELERATOR_TYPE":    accType,
				"E2E_ACCELERATOR_PRODUCT": accProduct,
				"E2E_ENGINE_ARGS_YAML":    engineArgsYAML(),
			}

			yamlPath, err := renderTemplateToTempFile("testdata/endpoint.yaml", defaults)
			Expect(err).NotTo(HaveOccurred())
			defer os.Remove(yamlPath)

			r := RunCLI("apply", "-f", yamlPath)
			ExpectSuccess(r)

			waitEndpointFailed(epName)
		})

		It("should not reach Running when resources exceed capacity", Label("C2613502"), func() {
			epName := "e2e-ep-ssh-bigres-" + Cfg.RunID
			DeferCleanup(func() { deleteEndpoint(epName) })

			yamlPath := applyEndpointOnCluster(epName, clusterName, profileEngineVersion())
			defer os.Remove(yamlPath)

			content, err := os.ReadFile(yamlPath)
			Expect(err).NotTo(HaveOccurred())
			patched := strings.Replace(string(content), "gpu: \"1\"", "gpu: \"100\"", 1)
			Expect(os.WriteFile(yamlPath, []byte(patched), 0o644)).To(Succeed())

			r := RunCLI("apply", "-f", yamlPath, "--force-update")
			ExpectSuccess(r)

			// Poll for 2 minutes — endpoint should stay in Pending/Deploying/Failed, never Running.
			Consistently(func() string {
				r = RunCLI("get", "endpoint", epName, "-w", profileWorkspace(), "-o", "json")
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
		})

		It("should delete a running endpoint", Label("C2612951", "C2612923"), func() {
			epName := "e2e-ep-ssh-delrun-" + Cfg.RunID
			DeferCleanup(func() { deleteEndpoint(epName) })

			yamlPath := applyEndpointOnCluster(epName, clusterName, profileEngineVersion())
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)
			deleteEndpoint(epName)

			r := RunCLI("get", "endpoint", epName, "-w", profileWorkspace(), "-o", "json")
			ExpectFailed(r)
		})

		It("should delete a failed endpoint", Label("C2612952"), func() {
			epName := "e2e-ep-ssh-delfail-" + Cfg.RunID
			DeferCleanup(func() { deleteEndpoint(epName) })

			yamlPath := applyFailingEndpoint(epName, clusterName)
			defer os.Remove(yamlPath)

			waitEndpointFailed(epName)
			deleteEndpoint(epName)

			r := RunCLI("get", "endpoint", epName, "-w", profileWorkspace(), "-o", "json")
			ExpectFailed(r)
		})

		It("should delete a pending endpoint", Label("C2612953"), func() {
			epName := "e2e-ep-ssh-delpend-" + Cfg.RunID
			DeferCleanup(func() { deleteEndpoint(epName) })

			yamlPath := applyEndpointOnCluster(epName, clusterName, profileEngineVersion())
			defer os.Remove(yamlPath)

			deleteEndpoint(epName)

			r := RunCLI("get", "endpoint", epName, "-w", profileWorkspace(), "-o", "json")
			ExpectFailed(r)
		})

		It("should verify resources are cleaned up after deletion", Label("C2613295"), func() {
			epName := "e2e-ep-ssh-cleanup-" + Cfg.RunID
			DeferCleanup(func() { deleteEndpoint(epName) })

			yamlPath := applyEndpointOnCluster(epName, clusterName, profileEngineVersion())
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)
			deleteEndpoint(epName)

			r := RunCLI("get", "endpoint", "-w", profileWorkspace())
			ExpectSuccess(r)
			Expect(r.Stdout).NotTo(ContainSubstring(epName))
		})
	})

	// --- Error Handling ---

	Describe("Error Handling", Label("error"), func() {

		It("should not panic when deployment_options is missing", Label("C2642242"), func() {
			epName := "e2e-ep-ssh-noopts-" + Cfg.RunID
			DeferCleanup(func() { deleteEndpoint(epName) })

			yamlPath := applyEndpointOnCluster(epName, clusterName, profileEngineVersion())
			defer os.Remove(yamlPath)

			r := RunCLI("get", "endpoint", epName, "-w", profileWorkspace(), "-o", "json")
			ExpectSuccess(r)
		})

		It("should show Failed when using unsupported engine", Label("C2612936"), func() {
			epName := "e2e-ep-ssh-badeng-" + Cfg.RunID
			DeferCleanup(func() { deleteEndpoint(epName) })

			accType, accProduct := getClusterAccelerator(clusterName)

			defaults := map[string]string{
				"E2E_ENDPOINT_NAME":       epName,
				"E2E_WORKSPACE":           profileWorkspace(),
				"E2E_CLUSTER_NAME":        clusterName,
				"E2E_ENGINE_NAME":         "nonexistent-engine-" + Cfg.RunID,
				"E2E_ENGINE_VERSION":      "v0.0.1",
				"E2E_MODEL_REGISTRY":      testRegistry(),
				"E2E_MODEL_NAME":          profileModelName(),
				"E2E_MODEL_VERSION":       profileModelVersion(),
				"E2E_MODEL_TASK":          profileModelTask(),
				"E2E_ACCELERATOR_TYPE":    accType,
				"E2E_ACCELERATOR_PRODUCT": accProduct,
				"E2E_ENGINE_ARGS_YAML":    engineArgsYAML(),
			}

			yamlPath, err := renderTemplateToTempFile("testdata/endpoint.yaml", defaults)
			Expect(err).NotTo(HaveOccurred())
			defer os.Remove(yamlPath)

			r := RunCLI("apply", "-f", yamlPath)
			if r.ExitCode == 0 {
				waitEndpointFailed(epName)
			}
		})

		It("should show Failed when no matching accelerator product", Label("C2613503"), func() {
			epName := "e2e-ep-ssh-badacc-" + Cfg.RunID
			DeferCleanup(func() { deleteEndpoint(epName) })

			accType, _ := getClusterAccelerator(clusterName)

			defaults := map[string]string{
				"E2E_ENDPOINT_NAME":       epName,
				"E2E_WORKSPACE":           profileWorkspace(),
				"E2E_CLUSTER_NAME":        clusterName,
				"E2E_ENGINE_NAME":         profileEngineName(),
				"E2E_ENGINE_VERSION":      profileEngineVersion(),
				"E2E_MODEL_REGISTRY":      testRegistry(),
				"E2E_MODEL_NAME":          profileModelName(),
				"E2E_MODEL_VERSION":       profileModelVersion(),
				"E2E_MODEL_TASK":          profileModelTask(),
				"E2E_ACCELERATOR_TYPE":    accType,
				"E2E_ACCELERATOR_PRODUCT": "NONEXISTENT-GPU-9999",
				"E2E_ENGINE_ARGS_YAML":    engineArgsYAML(),
			}

			yamlPath, err := renderTemplateToTempFile("testdata/endpoint.yaml", defaults)
			Expect(err).NotTo(HaveOccurred())
			defer os.Remove(yamlPath)

			r := RunCLI("apply", "-f", yamlPath)
			if r.ExitCode == 0 {
				waitEndpointFailed(epName)
			}
		})

		It("should show Failed when deployment is unhealthy", Label("C2642243"), func() {
			epName := "e2e-ep-ssh-unhealth-" + Cfg.RunID
			DeferCleanup(func() { deleteEndpoint(epName) })

			yamlPath := applyEndpointOnCluster(epName, clusterName, profileEngineVersion())
			defer os.Remove(yamlPath)

			content, err := os.ReadFile(yamlPath)
			Expect(err).NotTo(HaveOccurred())
			patched := strings.Replace(string(content), "gpu: \"1\"", "gpu: \"99\"", 1)
			Expect(os.WriteFile(yamlPath, []byte(patched), 0o644)).To(Succeed())

			r := RunCLI("apply", "-f", yamlPath, "--force-update")
			ExpectSuccess(r)

			waitEndpointFailed(epName)
		})

		It("should reject accelerator with non-string values", Label("C2642283"), func() {
			epName := "e2e-ep-ssh-badacc-type-" + Cfg.RunID
			DeferCleanup(func() { deleteEndpoint(epName) })

			accType, _ := getClusterAccelerator(clusterName)

			yaml := `apiVersion: v1
kind: Endpoint
metadata:
  name: ` + epName + `
  workspace: ` + profileWorkspace() + `
spec:
  cluster: ` + clusterName + `
  engine:
    engine: ` + profileEngineName() + `
    version: ` + profileEngineVersion() + `
  model:
    registry: ` + testRegistry() + `
    name: ` + profileModelName() + `
    version: ""
    task: ` + profileModelTask() + `
  resources:
    gpu: "1"
    accelerator:
      type: ` + accType + `
      product: {"nested": "object"}
  replicas:
    num: 1
`
			tmpFile, err := os.CreateTemp("", "e2e-badacc-*.yaml")
			Expect(err).NotTo(HaveOccurred())
			_, err = tmpFile.WriteString(yaml)
			Expect(err).NotTo(HaveOccurred())
			tmpFile.Close()
			defer os.Remove(tmpFile.Name())

			r := RunCLI("apply", "-f", tmpFile.Name())
			ExpectFailed(r)
		})

		It("should accept accelerator with valid string map", Label("C2642284"), func() {
			epName := "e2e-ep-ssh-goodacc-" + Cfg.RunID
			DeferCleanup(func() { deleteEndpoint(epName) })

			accType, accProduct := getClusterAccelerator(clusterName)

			yaml := `apiVersion: v1
kind: Endpoint
metadata:
  name: ` + epName + `
  workspace: ` + profileWorkspace() + `
spec:
  cluster: ` + clusterName + `
  engine:
    engine: ` + profileEngineName() + `
    version: ` + profileEngineVersion() + `
  model:
    registry: ` + testRegistry() + `
    name: ` + profileModelName() + `
    version: ""
    task: ` + profileModelTask() + `
  resources:
    gpu: "1"
    accelerator:
      type: ` + accType + `
      product: ` + accProduct + `
  replicas:
    num: 1
`
			tmpFile, err := os.CreateTemp("", "e2e-goodacc-*.yaml")
			Expect(err).NotTo(HaveOccurred())
			_, err = tmpFile.WriteString(yaml)
			Expect(err).NotTo(HaveOccurred())
			tmpFile.Close()
			defer os.Remove(tmpFile.Name())

			r := RunCLI("apply", "-f", tmpFile.Name())
			ExpectSuccess(r)

			r = RunCLI("get", "endpoint", epName, "-w", profileWorkspace(), "-o", "json")
			ExpectSuccess(r)
		})
	})

})
