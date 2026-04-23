package e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("K8s Endpoint", Ordered, Label("endpoint", "k8s"), func() {
	var clusterName string

	BeforeAll(func() {
		if profileModelName() == "" {
			Skip("Model name not configured in profile, skipping K8s endpoint tests")
		}

		clusterName = setupK8sCluster("e2e-ep-k8s-")

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
			epName = "e2e-ep-k8s-chat-" + Cfg.RunID
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

		It("should serve inference requests", func() {
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
	})

	// --- Multi-Version Isolation ---

	Describe("Multi-Version Isolation", Ordered, Label("inference", "multi-version"), func() {
		var epNameA, epNameB string

		BeforeAll(func() {
			if profileEngineOldVersion() == "" {
				Skip("engine.old_version not configured, skipping multi-version test")
			}

			// Only vLLM v0.11.2+ has K8s deploy templates; v0.8.5 does not.
			if profileEngineOldVersion() != profileEngineVersion() &&
				!engineVersionSupportsK8s(profileEngineOldVersion()) {
				Skip("engine.old_version " + profileEngineOldVersion() + " does not support K8s deployment")
			}

			epNameA = "e2e-ep-k8s-va-" + Cfg.RunID
			epNameB = "e2e-ep-k8s-vb-" + Cfg.RunID
		})

		AfterAll(func() {
			deleteEndpoint(epNameA)
			deleteEndpoint(epNameB)
		})

		It("should run two endpoints with different engine versions", func() {
			// Deploy sequentially to avoid GPU resource contention on K8s.
			yamlA := applyEndpointOnCluster(epNameA, clusterName, profileEngineOldVersion())
			defer os.Remove(yamlA)
			waitEndpointRunning(epNameA)

			yamlB := applyEndpointOnCluster(epNameB, clusterName, profileEngineVersion())
			defer os.Remove(yamlB)
			waitEndpointRunning(epNameB)

			epA := getEndpoint(epNameA)
			epB := getEndpoint(epNameB)
			Expect(epA.Status.Phase).To(BeEquivalentTo("Running"))
			Expect(epB.Status.Phase).To(BeEquivalentTo("Running"))
		})

		It("should serve inference from both endpoints", func() {
			epA := getEndpoint(epNameA)
			epB := getEndpoint(epNameB)

			codeA, bodyA := inferChat(epA.Status.ServiceURL, "Hello")
			Expect(codeA).To(Equal(http.StatusOK), "inference on ep-A failed: %s", bodyA)

			codeB, bodyB := inferChat(epB.Status.ServiceURL, "Hello")
			Expect(codeB).To(Equal(http.StatusOK), "inference on ep-B failed: %s", bodyB)
		})
	})

	// --- Tensor Parallel (TP=2) ---

	Describe("Tensor Parallel TP=2", Ordered, Label("inference", "tp2"), func() {
		var epName string

		BeforeAll(func() {
			epName = "e2e-ep-k8s-tp2-" + Cfg.RunID
		})

		AfterAll(func() {
			if epName != "" {
				deleteEndpoint(epName)
			}
		})

		It("should deploy with tp=2 (gpu=2) and reach Running", Label("C2613759"), func() {
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
		})

		It("should serve inference with tp=2", func() {
			ep := getEndpoint(epName)
			code, body := inferChat(ep.Status.ServiceURL, "Hello with TP=2")
			Expect(code).To(Equal(http.StatusOK), "inference with tp=2 failed: %s", body)
			Expect(body).To(ContainSubstring("choices"))
		})
	})

	// --- Embedding Inference ---

	Describe("Embedding Inference", Ordered, Label("inference", "embedding"), func() {
		var epName string

		BeforeAll(func() {
			if profileEmbeddingModelName() == "" {
				Skip("embedding_model.name not configured")
			}

			epName = "e2e-ep-k8s-embed-" + Cfg.RunID
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
			epName = "e2e-ep-k8s-rerank-" + Cfg.RunID
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

	// --- vLLM v0.17.1 Inference ---

	Describe("vLLM v0.17.1 Inference", Ordered, Label("inference", "v0171"), func() {
		var epName string

		BeforeAll(func() {
			requireEngineVersion("vllm", "v0.17.1")
			epName = "e2e-ep-k8s-v0171-" + Cfg.RunID
		})

		AfterAll(func() {
			if epName != "" {
				deleteEndpoint(epName)
			}
		})

		It("should deploy with vLLM v0.17.1 and serve chat requests", Label("C2644371"), func() {
			yamlPath := applyEndpointOnCluster(epName, clusterName, "v0.17.1")
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Running"))

			code, body := inferChat(ep.Status.ServiceURL, "Hello from v0.17.1")
			Expect(code).To(Equal(http.StatusOK), "v0.17.1 chat inference failed: %s", body)
			Expect(body).To(ContainSubstring("choices"))
		})
	})

	Describe("vLLM v0.17.1 Embedding", Ordered, Label("inference", "v0171", "embedding"), func() {
		var epName string

		BeforeAll(func() {
			requireEngineVersion("vllm", "v0.17.1")
			if profileEmbeddingModelName() == "" {
				Skip("embedding_model.name not configured")
			}
			epName = "e2e-ep-k8s-v0171-embed-" + Cfg.RunID
		})

		AfterAll(func() {
			if epName != "" {
				deleteEndpoint(epName)
			}
		})

		It("should deploy and serve embedding requests with v0.17.1", Label("C2644372"), func() {
			yamlPath := applyEndpointWithTask(epName, clusterName, "v0.17.1",
				profileEmbeddingModelName(), profileEmbeddingModelVersion(), "text-embedding", "")
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			code, body := inferEmbedding(ep.Status.ServiceURL, profileEmbeddingModelName(), "Hello world")
			Expect(code).To(Equal(http.StatusOK), "v0.17.1 embedding failed: %s", body)
		})
	})

	Describe("vLLM v0.17.1 Rerank", Ordered, Label("inference", "v0171", "rerank"), func() {
		var epName string

		BeforeAll(func() {
			requireEngineVersion("vllm", "v0.17.1")
			if profileRerankModelName() == "" {
				Skip("rerank_model.name not configured")
			}
			epName = "e2e-ep-k8s-v0171-rerank-" + Cfg.RunID
		})

		AfterAll(func() {
			if epName != "" {
				deleteEndpoint(epName)
			}
		})

		It("should deploy and serve rerank requests with v0.17.1", Label("C2644373"), func() {
			yamlPath := applyEndpointWithTask(epName, clusterName, "v0.17.1",
				profileRerankModelName(), profileRerankModelVersion(), "text-rerank", "")
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			code, body := inferRerank(ep.Status.ServiceURL, profileRerankModelName(),
				"What is the capital of France?", []string{"Paris is the capital.", "Berlin is in Germany."})
			Expect(code).To(Equal(http.StatusOK), "v0.17.1 rerank failed: %s", body)
		})
	})

	// --- Status & Delete ---

	Describe("Status & Delete", Label("status"), func() {

		It("should show Failed when model does not exist", Label("C2612944"), func() {
			epName := "e2e-ep-k8s-fail-" + Cfg.RunID
			DeferCleanup(func() { deleteEndpoint(epName) })

			yamlPath := applyFailingEndpoint(epName, clusterName)
			defer os.Remove(yamlPath)

			waitEndpointFailed(epName)
			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Failed"))
		})

		It("should show Failed when model version does not exist", Label("C2613501"), func() {
			epName := "e2e-ep-k8s-badver-" + Cfg.RunID
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

		It("should delete a running endpoint", Label("C2612951", "C2612923"), func() {
			epName := "e2e-ep-k8s-delrun-" + Cfg.RunID
			DeferCleanup(func() { deleteEndpoint(epName) })

			yamlPath := applyEndpointOnCluster(epName, clusterName, profileEngineVersion())
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)
			deleteEndpoint(epName)

			r := RunCLI("get", "endpoint", epName, "-w", profileWorkspace(), "-o", "json")
			ExpectFailed(r)
		})

		It("should recreate endpoint with same name after deletion", Label("C2644061"), func() {
			epName := "e2e-ep-k8s-recreate-" + Cfg.RunID
			DeferCleanup(func() { deleteEndpoint(epName) })

			// Create first instance
			yamlPath := applyEndpointOnCluster(epName, clusterName, profileEngineVersion())
			os.Remove(yamlPath)
			waitEndpointRunning(epName)

			// Graceful delete (not force) to allow proper K8s resource cleanup
			RunCLI("delete", "endpoint", epName, "-w", profileWorkspace())
			RunCLI("wait", "endpoint", epName,
				"-w", profileWorkspace(),
				"--for", "delete",
				"--timeout", "5m",
			)

			// Recreate with same name
			yamlPath = applyEndpointOnCluster(epName, clusterName, profileEngineVersion())
			defer os.Remove(yamlPath)
			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Running"))
		})
	})

})
