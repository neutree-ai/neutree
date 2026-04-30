package e2e

import (
	"encoding/json"
	"net/http"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// SGLang K8s endpoint cases for NEU-429.
//
// TestRail: C2649559 (chat), C2649560 (embedding), C2649561 (rerank),
// C2649562 (engine_args multi-type forwarding). Cases are scoped via the
// "sglang" Ginkgo Label so callers can run only this file with
// --ginkgo.label-filter='sglang'.
//
// All four cases share one cluster + one model registry to keep total runtime
// bounded; they live in a single Ordered Describe so they execute sequentially
// and don't fight for the test cluster's GPU.
var _ = Describe("K8s SGLang Endpoint", Ordered, Label("endpoint", "k8s", "sglang"), func() {
	var clusterName string

	BeforeAll(func() {
		if profileModelName() == "" {
			Skip("Model name not configured in profile, skipping SGLang K8s tests")
		}

		clusterName = setupK8sCluster("e2e-sglang-")

		By("Setting up model registry")
		SetupModelRegistry()
	})

	AfterAll(func() {
		TeardownModelRegistry()
		teardownCluster(clusterName)
	})

	// --- Case 1: chat completion ---

	Describe("Chat Completion", Ordered, Label("inference", "chat"), func() {
		var epName string

		BeforeAll(func() {
			epName = "e2e-sglang-chat-" + Cfg.RunID
		})

		AfterAll(func() {
			if epName != "" {
				deleteEndpoint(epName)
			}
		})

		It("should deploy SGLang v0.5.10 endpoint and serve chat completion", Label("C2649559"), func() {
			yamlPath := applyEndpoint(epName, clusterName,
				withEngine("sglang", "v0.5.10"))
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Running"))
			Expect(ep.Status.ServiceURL).NotTo(BeEmpty())

			code, body, err := inferChat(ep.Status.ServiceURL, "Hello")
			Expect(err).NotTo(HaveOccurred())
			Expect(code).To(Equal(http.StatusOK), "chat completion failed: %s", body)
			Expect(body).To(ContainSubstring("choices"))
		})
	})

	// --- Case 2: embedding ---

	Describe("Embedding", Ordered, Label("inference", "embedding"), func() {
		var epName string

		BeforeAll(func() {
			if profileEmbeddingModelName() == "" {
				Skip("embedding_model.name not configured in profile, skipping")
			}
			epName = "e2e-sglang-embed-" + Cfg.RunID
		})

		AfterAll(func() {
			if epName != "" {
				deleteEndpoint(epName)
			}
		})

		It("should deploy SGLang embedding endpoint and serve /v1/embeddings", Label("C2649560"), func() {
			yamlPath := applyEndpoint(epName, clusterName,
				withEngine("sglang", "v0.5.10"),
				withModel(profileEmbeddingModelName(), profileEmbeddingModelVersion()),
				withTask("text-embedding"))
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)

			ep := getEndpoint(epName)

			By("single-string embedding")
			code, body, err := inferEmbedding(ep.Status.ServiceURL, profileEmbeddingModelName(), "Hello world")
			Expect(err).NotTo(HaveOccurred())
			Expect(code).To(Equal(http.StatusOK), "single embedding failed: %s", body)

			var single map[string]any
			Expect(json.Unmarshal([]byte(body), &single)).To(Succeed())
			Expect(single).To(HaveKey("data"))

			By("batch embedding (>=4 inputs)")
			code, body, err = doInferenceRequest(ep.Status.ServiceURL, "/v1/embeddings", map[string]any{
				"model": profileEmbeddingModelName(),
				"input": []string{"alpha", "beta", "gamma", "delta"},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(code).To(Equal(http.StatusOK), "batch embedding failed: %s", body)

			var batch map[string]any
			Expect(json.Unmarshal([]byte(body), &batch)).To(Succeed())
			Expect(batch).To(HaveKey("data"))
			data, ok := batch["data"].([]any)
			Expect(ok).To(BeTrue(), "data must be a list, got: %s", body)
			Expect(data).To(HaveLen(4), "expected 4 embeddings, got %d", len(data))
		})
	})

	// --- Case 3: rerank ---

	Describe("Rerank", Ordered, Label("inference", "rerank"), func() {
		var epName string

		BeforeAll(func() {
			if profileRerankModelName() == "" {
				Skip("rerank_model.name not configured in profile, skipping")
			}
			epName = "e2e-sglang-rerank-" + Cfg.RunID
		})

		AfterAll(func() {
			if epName != "" {
				deleteEndpoint(epName)
			}
		})

		It("should deploy SGLang rerank endpoint and serve /v1/rerank", Label("C2649561"), func() {
			yamlPath := applyEndpoint(epName, clusterName,
				withEngine("sglang", "v0.5.10"),
				withModel(profileRerankModelName(), profileRerankModelVersion()),
				withTask("text-rerank"))
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			docs := []string{
				"Paris is the capital of France.",
				"Berlin is in Germany.",
				"Lyon is a French city.",
			}
			code, body, err := inferRerank(ep.Status.ServiceURL, profileRerankModelName(),
				"What is the capital of France?", docs)
			Expect(err).NotTo(HaveOccurred())
			Expect(code).To(Equal(http.StatusOK), "rerank failed: %s", body)

			var resp map[string]any
			Expect(json.Unmarshal([]byte(body), &resp)).To(Succeed())
			Expect(resp).To(HaveKey("results"))

			results, ok := resp["results"].([]any)
			Expect(ok).To(BeTrue(), "results must be a list, got: %s", body)
			Expect(results).To(HaveLen(len(docs)))

			// Each entry must carry relevance_score (float) + index (int).
			var lastScore float64
			for i, r := range results {
				m, ok := r.(map[string]any)
				Expect(ok).To(BeTrue(), "result[%d] must be an object", i)
				Expect(m).To(HaveKey("relevance_score"))
				Expect(m).To(HaveKey("index"))

				score, ok := m["relevance_score"].(float64)
				Expect(ok).To(BeTrue(), "relevance_score must be a number, got %T", m["relevance_score"])
				if i > 0 {
					Expect(score).To(BeNumerically("<=", lastScore),
						"results must be sorted by relevance_score descending")
				}
				lastScore = score
			}
		})
	})

	// --- Case 4: engine_args multi-type forwarding ---

	Describe("EngineArgs Multi-Type Forwarding", Ordered, Label("inference", "engine-args"), func() {
		var epName string

		BeforeAll(func() {
			epName = "e2e-sglang-args-" + Cfg.RunID
		})

		AfterAll(func() {
			if epName != "" {
				deleteEndpoint(epName)
			}
		})

		It("should accept int/float/bool/string/enum/array/object engine_args end-to-end", Label("C2649562"), func() {
			yamlPath := applyEndpoint(epName, clusterName,
				withEngine("sglang", "v0.5.10"),
				withEngineArgs(allSchemaTypesEngineArgsYAMLSGLang()))
			defer os.Remove(yamlPath)

			// Reaching Running is itself the strongest signal: SGLang ServerArgs
			// validates every flag at startup. If int/float/bool/string/enum/array
			// or object values were mangled by the K8s template renderer (e.g.
			// underscore→kebab miss, JSON quoting torn by shell, unknown flag),
			// the engine would fail-on-unknown and the pod would CrashLoop
			// before phase=Running.
			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Running"))

			// Cross-process probe: served-model-name reaches SGLang via CLI, and
			// SGLang echoes it back in chat completions. A successful HTTP 200
			// with response.model equal to the override proves the value
			// traversed the entire schema->template->CLI->ServerArgs chain.
			By("served-model-name probe via chat completion")
			code, body, err := doInferenceRequest(ep.Status.ServiceURL, "/v1/chat/completions", map[string]any{
				"model": "neu-sglang-test",
				"messages": []map[string]string{
					{"role": "user", "content": "Hi"},
				},
				"max_tokens": 8,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(code).To(Equal(http.StatusOK), "chat with custom served-model-name failed: %s", body)

			var resp map[string]any
			Expect(json.Unmarshal([]byte(body), &resp)).To(Succeed())
			Expect(resp["model"]).To(Equal("neu-sglang-test"),
				"response.model must echo served-model-name override; body=%s", body)
		})
	})
})
