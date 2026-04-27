package e2e

import (
	"encoding/json"
	"net/http"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
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

		// C2642267 step 1: single-GPU deploy + Running (step 2-3: multi-GPU TP → C2613759, C2642248)
		It("should deploy with engine container and reach Running", Label("C2613491", "C2642267"), func() {
			yamlPath := applyEndpoint(epName, clusterName)
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Running"))
			Expect(ep.Spec.Engine.Version).To(Equal(profileEngineVersion()))
			Expect(ep.Status.ServiceURL).NotTo(BeEmpty())
		})

		It("should serve inference requests", func() {
			ep := getEndpoint(epName)
			code, body, err := inferChat(ep.Status.ServiceURL, "Hello")
			Expect(err).NotTo(HaveOccurred())
			Expect(code).To(Equal(http.StatusOK), "inference failed: %s", body)
			Expect(body).To(ContainSubstring("choices"))
		})

		It("should return error for wrong model name", Label("inference-error"), func() {
			ep := getEndpoint(epName)

			By("Sending request with non-existent model name")
			code, body, err := doInferenceRequest(ep.Status.ServiceURL, "/v1/chat/completions", map[string]any{
				"model": "non-existent-model-name",
				"messages": []map[string]string{
					{"role": "user", "content": "hello"},
				},
				"max_tokens": 8,
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(code).To(BeElementOf(http.StatusBadRequest, http.StatusNotFound),
				"request with wrong model name should return 400 or 404, got %d, body: %s", code, body)
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
			yamlA := applyEndpoint(epNameA, clusterName, withEngineVersion(profileEngineOldVersion()))
			defer os.Remove(yamlA)
			waitEndpointRunning(epNameA)

			yamlB := applyEndpoint(epNameB, clusterName)
			defer os.Remove(yamlB)
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

			codeA, bodyA, err := inferChat(epA.Status.ServiceURL, "Hello")
			Expect(err).NotTo(HaveOccurred())
			Expect(codeA).To(Equal(http.StatusOK), "inference on ep-A failed: %s", bodyA)

			codeB, bodyB, err := inferChat(epB.Status.ServiceURL, "Hello")
			Expect(err).NotTo(HaveOccurred())
			Expect(codeB).To(Equal(http.StatusOK), "inference on ep-B failed: %s", bodyB)
		})

		It("should not affect other endpoint when deleting one", Label("C2642252"), func() {
			// Delete endpoint A (old version)
			deleteEndpoint(epNameA)

			// Verify endpoint B (new version) still works
			epB := getEndpoint(epNameB)
			Expect(epB.Status.Phase).To(BeEquivalentTo("Running"))

			codeB, bodyB, err := inferChat(epB.Status.ServiceURL, "Hello after delete")
			Expect(err).NotTo(HaveOccurred())
			Expect(codeB).To(Equal(http.StatusOK), "inference on ep-B after deleting ep-A failed: %s", bodyB)
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

		// C2642267 step 2-3: multi-GPU TP deploy + inference
		It("should deploy with tp=2 (gpu=2) and reach Running", Label("C2613759", "C2642248", "C2642267"), func() {
			tpArgs := engineArgsYAML() + "\n      tensor_parallel_size: 2"
			yamlPath := applyEndpoint(epName, clusterName,
				withGPU("2"), withEngineArgs(tpArgs))
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Running"))
			Expect(ep.Status.ServiceURL).NotTo(BeEmpty())

			By("Verifying tensor_parallel_size=2 in Ray Serve config")
			c := getClusterFullJSON(clusterName)
			rayH := NewRayHelper(c.Status.DashboardURL)

			appName := profileWorkspace() + "_" + epName
			appConfig, err := rayH.GetApplicationConfig(appName)
			Expect(err).NotTo(HaveOccurred())
			Expect(appConfig).NotTo(BeNil(), "application %s should exist", appName)

			engineArgs, ok := appConfig.Args["engine_args"].(map[string]interface{})
			Expect(ok).To(BeTrue(), "engine_args should exist")

			tp, ok := engineArgs["tensor_parallel_size"]
			Expect(ok).To(BeTrue(), "tensor_parallel_size should exist in engine_args")
			Expect(tp).To(BeNumerically("==", 2),
				"tensor_parallel_size should be 2 (user-specified value)")
		})

		It("should serve inference with tp=2", func() {
			ep := getEndpoint(epName)
			code, body, err := inferChat(ep.Status.ServiceURL, "Hello with TP=2")
			Expect(err).NotTo(HaveOccurred())
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
			yamlPath := applyEndpoint(epName, clusterName, withGPU("2"))
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Running"))

			// Verify inference works
			code, body, err := inferChat(ep.Status.ServiceURL, "Hello auto-TP")
			Expect(err).NotTo(HaveOccurred())
			Expect(code).To(Equal(http.StatusOK), "inference with auto-TP failed: %s", body)

			By("Verifying tensor_parallel_size auto-set to GPU count (2) in Ray Serve config")
			c := getClusterFullJSON(clusterName)
			rayH := NewRayHelper(c.Status.DashboardURL)

			appName := profileWorkspace() + "_" + epName
			appConfig, err := rayH.GetApplicationConfig(appName)
			Expect(err).NotTo(HaveOccurred())
			Expect(appConfig).NotTo(BeNil(), "application %s should exist", appName)

			engineArgs, ok := appConfig.Args["engine_args"].(map[string]interface{})
			Expect(ok).To(BeTrue(), "engine_args should exist")

			tp, ok := engineArgs["tensor_parallel_size"]
			Expect(ok).To(BeTrue(), "tensor_parallel_size should exist in engine_args")
			Expect(tp).To(BeNumerically("==", 2),
				"tensor_parallel_size should be auto-set to GPU count (2)")
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
			yamlPath := applyEndpoint(epName, clusterName,
				withModel(profileEmbeddingModelName(), profileEmbeddingModelVersion()),
				withTask("text-embedding"))
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			code, body, err := inferEmbedding(ep.Status.ServiceURL, profileEmbeddingModelName(), "Hello world")
			Expect(err).NotTo(HaveOccurred())
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
			yamlPath := applyEndpoint(epName, clusterName,
				withModel(profileRerankModelName(), profileRerankModelVersion()),
				withTask("text-rerank"))
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			code, body, err := inferRerank(ep.Status.ServiceURL, profileRerankModelName(),
				"What is the capital of France?", []string{"Paris is the capital of France.", "Berlin is in Germany."})
			Expect(err).NotTo(HaveOccurred())
			Expect(code).To(Equal(http.StatusOK), "rerank inference failed: %s", body)

			var resp map[string]any
			Expect(json.Unmarshal([]byte(body), &resp)).To(Succeed())
			Expect(resp).To(HaveKey("results"))
		})
	})

})
