package e2e

import (
	"encoding/json"
	"net/http"
	"os"
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

		It("should accept opaque x-request-id headers", Label("C2723107", "request-id"), func() {
			requestIDEPName := "e2e-ep-ssh-reqid-" + Cfg.RunID
			yamlPath := applyEndpoint(requestIDEPName, clusterName)
			defer os.Remove(yamlPath)
			defer deleteEndpoint(requestIDEPName)

			waitEndpointRunning(requestIDEPName)
			ep := getEndpoint(requestIDEPName)
			code, body, err := inferChatWithHeaders(ep.Status.ServiceURL, "Hello", map[string]string{
				"x-request-id": "bench-NEU-454-10",
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(code).To(Equal(http.StatusOK), "inference failed: %s", body)
			Expect(body).To(ContainSubstring("choices"))
		})

		It("should return error for wrong model name", Label("inference-error"), func() {
			ep := getEndpoint(epName)

			By("Sending request with non-existent model name")
			code, body, err := doInferenceRequest(ep.Status.ServiceURL, "/v1/chat/completions", map[string]any{
				"model": "non-existent-model-name",
				"messages": []map[string]any{
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

	// --- Sibling Endpoint Update Isolation ---
	//
	// Ray Serve PUT /api/serve/applications/ unconditionally writes
	// ApplicationStatus to DEPLOYING for every application in the request,
	// even those whose configs are unchanged. Without the suppression in
	// GetEndpointStatus this leaks to Neutree as a transient Running →
	// Deploying → Running flicker on sibling endpoints whenever any single
	// endpoint in the same cluster is updated. See ray-project/ray#25381,
	// #42974, #44226.
	//
	// Lives under the inference-test file purely to reuse the SSH cluster
	// fixture; the case itself is a status-reporting assertion.
	Describe("Sibling Endpoint Update Isolation", Ordered, Label("status", "isolation"), func() {
		var epNameA, epNameB string

		BeforeAll(func() {
			epNameA = "e2e-ep-ssh-iso-a-" + Cfg.RunID
			epNameB = "e2e-ep-ssh-iso-b-" + Cfg.RunID
		})

		AfterAll(func() {
			deleteEndpoint(epNameA)
			deleteEndpoint(epNameB)
		})

		It("should keep sibling endpoint Running when one endpoint is updated", Label("C2650084"), func() {
			By("Deploying endpoint A and waiting for Running")
			yamlA := applyEndpoint(epNameA, clusterName)
			defer os.Remove(yamlA)
			waitEndpointRunning(epNameA)

			By("Deploying endpoint B and waiting for Running")
			yamlB := applyEndpoint(epNameB, clusterName)
			defer os.Remove(yamlB)
			waitEndpointRunning(epNameB)

			By("Recording endpoint B baseline before updating endpoint A")
			epBBefore := getEndpoint(epNameB)
			Expect(epBBefore.Status.Phase).To(BeEquivalentTo("Running"))
			lastTransitionBefore := epBBefore.Status.LastTransitionTime

			By("Updating endpoint A (re-apply with extra env to force a Ray Serve PUT)")
			// withEnv injects a new key into the endpoint spec → the controller
			// detects a config diff and re-issues PUT /api/serve/applications/
			// against Ray. The PUT is what triggers the transient DEPLOYING
			// write on every application in the request; the env key itself is
			// a marker, the value is irrelevant.
			yamlAUpdate := applyEndpoint(epNameA, clusterName,
				withEnv(map[string]string{"E2E_ISOLATION_MARKER": "1"}))
			defer os.Remove(yamlAUpdate)

			By("Polling endpoint B every second for 90s — phase must stay Running")
			// Sampling-based assertion: catches any flicker the controller
			// reconciles slower than 1s. The definitive guard is the
			// LastTransitionTime equality check below — it catches any phase
			// write by the controller regardless of poll cadence.
			Consistently(func() v1.EndpointPhase {
				return getEndpoint(epNameB).Status.Phase
			}, 90*time.Second, 1*time.Second).
				Should(BeEquivalentTo("Running"),
					"endpoint B phase flickered while endpoint A was being updated")

			By("Waiting for endpoint A rollout to settle")
			waitEndpointRunning(epNameA)

			By("Sampling endpoint B for an additional 10s after A settled")
			Consistently(func() v1.EndpointPhase {
				return getEndpoint(epNameB).Status.Phase
			}, 10*time.Second, 1*time.Second).
				Should(BeEquivalentTo("Running"),
					"endpoint B phase flickered after endpoint A finished rollout")

			By("Verifying endpoint B LastTransitionTime did not change (definitive guard)")
			// LastTransitionTime is bumped only when the controller detects an
			// actual status change (shouldUpdateStatus → updateStatus). If
			// endpoint B's phase ever flipped — even momentarily between two
			// reconciles the sampling above couldn't catch — the controller
			// would have written the change and bumped LastTransitionTime.
			// Equality with the pre-update baseline therefore strictly proves
			// no phase write happened for endpoint B during A's update.
			epBAfter := getEndpoint(epNameB)
			Expect(epBAfter.Status.LastTransitionTime).To(Equal(lastTransitionBefore),
				"endpoint B LastTransitionTime changed (controller wrote an intermediate phase) — before=%q after=%q",
				lastTransitionBefore, epBAfter.Status.LastTransitionTime)
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
			tpArgs := append(engineArgs(profileEngineName()), EngineArg{Key: "tensor_parallel_size", Value: "2"})
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

	// --- SGLang cases ---
	//
	// Co-located with the vLLM/default-engine inference cases above so they
	// share one cluster + model registry. Filter via
	// --ginkgo.label-filter='endpoint && ssh && sglang' to run only these.

	Describe("SGLang Chat Completion", Ordered, Label("inference", "chat", "sglang"), func() {
		var epName string

		BeforeAll(func() {
			epName = "e2e-sglang-ssh-chat-" + Cfg.RunID
		})

		AfterAll(func() {
			if epName != "" {
				deleteEndpoint(epName)
			}
		})

		It("should deploy SGLang endpoint and serve chat completion", Label("C2649559"), func() {
			yamlPath := applyEndpoint(epName, clusterName,
				withEngine("sglang", profileEngineVersionFor("sglang")))
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

	Describe("SGLang Embedding", Ordered, Label("inference", "embedding", "sglang"), func() {
		var epName string

		BeforeAll(func() {
			if profileEmbeddingModelName() == "" {
				Skip("embedding_model.name not configured in profile, skipping")
			}
			epName = "e2e-sglang-ssh-embed-" + Cfg.RunID
		})

		AfterAll(func() {
			if epName != "" {
				deleteEndpoint(epName)
			}
		})

		It("should deploy SGLang embedding endpoint and serve /v1/embeddings", Label("C2649560"), func() {
			yamlPath := applyEndpoint(epName, clusterName,
				withEngine("sglang", profileEngineVersionFor("sglang")),
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

	// SGLang multi-type engine_args forwarding lives in
	// endpoint_ssh_config_test.go alongside vLLM's "All Schema Types Engine
	// Args" — same Ray-Serve-app-config verification style.

	Describe("SGLang Tensor Parallel TP=2", Ordered, Label("inference", "tp2", "sglang"), func() {
		var epName string

		BeforeAll(func() {
			epName = "e2e-sglang-ssh-tp2-" + Cfg.RunID
		})

		AfterAll(func() {
			if epName != "" {
				deleteEndpoint(epName)
			}
		})

		It("should deploy SGLang with tp_size=2 (gpu=2) and reach Running", Label("C2649563"), func() {
			tpArgs := []EngineArg{{Key: "tp_size", Value: "2"}}
			yamlPath := applyEndpoint(epName, clusterName,
				withEngine("sglang", profileEngineVersionFor("sglang")),
				withGPU("2"), withEngineArgs(tpArgs))
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Running"))
			Expect(ep.Status.ServiceURL).NotTo(BeEmpty())

			By("Verifying tp_size=2 in Ray Serve config")
			cluster := getClusterFullJSON(clusterName)
			rayH := NewRayHelper(cluster.Status.DashboardURL)

			appName := profileWorkspace() + "_" + epName
			appConfig, err := rayH.GetApplicationConfig(appName)
			Expect(err).NotTo(HaveOccurred())
			Expect(appConfig).NotTo(BeNil(), "application %s should exist", appName)

			engineArgs, ok := appConfig.Args["engine_args"].(map[string]interface{})
			Expect(ok).To(BeTrue(), "engine_args should exist")

			tp, ok := engineArgs["tp_size"]
			Expect(ok).To(BeTrue(), "tp_size should exist in engine_args")
			Expect(tp).To(BeNumerically("==", 2),
				"tp_size should be 2 (user-specified value)")
		})
	})

	Describe("SGLang Auto Tensor Parallel", Ordered, Label("inference", "auto-tp", "sglang"), func() {
		var epName string

		BeforeAll(func() {
			epName = "e2e-sglang-ssh-autotp-" + Cfg.RunID
		})

		AfterAll(func() {
			if epName != "" {
				deleteEndpoint(epName)
			}
		})

		It("should auto-set tp_size to GPU count when not specified", Label("C2649564"), func() {
			yamlPath := applyEndpoint(epName, clusterName,
				withEngine("sglang", profileEngineVersionFor("sglang")),
				withGPU("2"))
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Running"))

			By("Verifying tp_size auto-set to GPU count (2) in Ray Serve config")
			cluster := getClusterFullJSON(clusterName)
			rayH := NewRayHelper(cluster.Status.DashboardURL)

			appName := profileWorkspace() + "_" + epName
			appConfig, err := rayH.GetApplicationConfig(appName)
			Expect(err).NotTo(HaveOccurred())
			Expect(appConfig).NotTo(BeNil(), "application %s should exist", appName)

			engineArgs, ok := appConfig.Args["engine_args"].(map[string]interface{})
			Expect(ok).To(BeTrue(), "engine_args should exist")

			tp, ok := engineArgs["tp_size"]
			Expect(ok).To(BeTrue(), "tp_size should be auto-set in engine_args")
			Expect(tp).To(BeNumerically("==", 2),
				"tp_size should be auto-set to GPU count (2)")
		})
	})
})
