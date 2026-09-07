package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path"
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

		// C2642267 step 1: single-GPU deploy + Running (step 2-3: multi-GPU TP → C2613759, C2642248)
		It("should deploy with engine container and reach Running", Label("C2613491", "C2642267"), func() {
			yamlPath := applyEndpoint(epName, clusterName)
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Running"))
			Expect(ep.Spec.Engine.Version).To(Equal(profileEngineVersion()))
			Expect(ep.Status.ServiceURL).NotTo(BeEmpty())

			By("Verifying node-agent exposes Static Ray endpoint replica accelerator usage")
			assertStaticRayNodeAgentEndpointAcceleratorMetrics(clusterName, epName)

			By("Verifying StaticNode and endpoint resources reflect node-agent accelerator allocations")
			assertStaticRayEndpointAcceleratorResourceSync(clusterName, epName)
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

	// --- Model Alias Non-Disturbance (NEU-652) ---
	//
	// NEU-620 gave a model version a display alias. The alias is a label: nothing
	// in orchestration reads it and it never reaches spec.model.name, so renaming
	// it must not move a deployment that is already serving that model.
	//
	// A unit test can only show the handler wrote nothing to the registry. What
	// it cannot show — and what this covers — is that the Serve app was not
	// redeployed and the engine containers were not restarted underneath a
	// running endpoint. That acceptance item was previously carried by prose in a
	// PR description, which is not re-runnable and goes stale silently.
	//
	// Lives under the SSH inference file to reuse its cluster and model registry
	// fixture, exactly as "Sibling Endpoint Update Isolation" above does. The case
	// itself is an orchestration-stability assertion, not an inference one.
	Describe("Model Alias Non-Disturbance", Ordered, Label("model", "alias", "isolation"), func() {
		var (
			epName  string
			sshUser string
			keyFile string
			modelH  *ModelHelper
		)

		BeforeAll(func() {
			// Aliases only exist on a private registry — a public one is read-only
			// and has none, which the ticket puts out of scope.
			if profile.ModelRegistry.Type != v1.BentoMLModelRegistryType {
				Skip("model alias requires a private (bentoml) model registry")
			}

			epName = "e2e-ep-ssh-alias-" + Cfg.RunID
			sshUser = profileSSHUser()
			keyFile = expandHome(profile.SSHNodes[0].KeyFile)
			// A local instance rather than the package-level Model, which belongs
			// to the model suite's fixture and is nil here. It resolves to the same
			// registry the BeforeAll above set up.
			modelH = NewModelHelper()
		})

		AfterAll(func() {
			// Guarded because the BeforeAll above can Skip before epName is set,
			// and Ginkgo still runs AfterAll in that case. deleteEndpoint asserts
			// success, so an unguarded call would turn a clean skip on a
			// non-private registry into a failing suite. Same guard as the Chat
			// Inference block above.
			if epName != "" {
				deleteEndpoint(epName)
			}
		})

		// TestRail: pending — Model management / alias / changing the alias of a model a
		// running endpoint is serving must not disturb that endpoint
		It("should not disturb a running endpoint when the served model's alias changes",
			Label(needsTestRailID), func() {
				By("Deploying an endpoint and waiting for Running")
				yamlPath := applyEndpoint(epName, clusterName)
				defer os.Remove(yamlPath)
				waitEndpointRunning(epName)

				cluster := getClusterFullJSON(clusterName)
				Expect(cluster.Status.DashboardURL).NotTo(BeEmpty())

				rayH := NewRayHelper(cluster.Status.DashboardURL)
				appName := profileWorkspace() + "_" + epName

				By("Recording the orchestration baseline")
				before, err := rayH.GetServeAppSnapshot(appName)
				Expect(err).NotTo(HaveOccurred())
				engineImage := requireOrchestrationPath(before)

				nodes := getStaticNodesForCluster(clusterName)
				Expect(nodes).NotTo(BeEmpty(), "cluster %s reported no static nodes", clusterName)

				// Scopes the container probe to this endpoint. The engine image
				// alone does not: sibling endpoints on this cluster run the same
				// image, so a set comparison over "everything built from that
				// image" would turn their churn into a failure blamed on aliases.
				// The path is the one orchestration builds per endpoint; asserting
				// it is really in the live run options first means a change to that
				// convention fails here, loudly, instead of silently selecting
				// nothing and leaving the gate below to guess why.
				// path, not path/filepath: this is a mount destination inside a Linux
				// container on a remote node, not a path on whatever host runs the suite.
				registryMountPath := path.Join("/mnt", profileWorkspace(), epName)
				Expect(orchestrationPathValue(before, "backend_container.run_options")).
					To(ContainSubstring("dst="+registryMountPath),
						"the engine container does not mount the NFS registry at the per-endpoint path "+
							"this probe scopes on")

				containersBefore := engineContainersOnNodes(nodes, sshUser, keyFile, engineImage, registryMountPath)

				epBefore := getEndpoint(epName)
				Expect(epBefore.Status.Phase).To(BeEquivalentTo("Running"))
				lastTransitionBefore := epBefore.Status.LastTransitionTime

				modelName, modelVersion := profileModelName(), profileModelVersion()
				DeferCleanup(modelH.EnsureAliasCleared, modelName, modelVersion)

				By("Changing the alias of the model this endpoint is serving")
				// Set, rename, clear: the three shapes an alias write takes. Each is
				// followed by a sampling window, so a redeploy triggered by any one
				// of them is caught while it is happening rather than only in the
				// end-state comparison.
				//
				// That is three × 20s of deliberate waiting. It is the cost of the
				// sampling half of the assertion and it is intentional: a redeploy
				// that starts and finishes between two end-state reads would leave
				// last_deployed_time_s moved, but a phase flicker that resolves the
				// same way would not, and only sampling sees it. The end-state
				// checks below are the definitive guards; this is what covers the
				// window between them.
				for _, alias := range []string{
					"E2E Served " + Cfg.RunID,
					"E2E Served Renamed " + Cfg.RunID,
					"",
				} {
					body, status := modelH.SetAlias(modelName, modelVersion, alias)
					Expect(status).To(Equal(http.StatusOK),
						"failed to write alias %q on %s:%s: %s", alias, modelName, modelVersion, body)

					if alias != "" {
						By("Verifying the alias did not leak into the orchestration path")
						current, err := rayH.GetServeAppSnapshot(appName)
						Expect(err).NotTo(HaveOccurred())
						Expect(string(current.DeployedAppConfig)).NotTo(ContainSubstring(alias),
							"alias %q reached the deployed Serve config", alias)
					}

					By("Sampling endpoint phase for 20s after the alias write")
					Consistently(func() v1.EndpointPhase {
						return getEndpoint(epName).Status.Phase
					}, 20*time.Second, 1*time.Second).
						Should(BeEquivalentTo("Running"),
							"endpoint phase flickered after the alias was set to %q", alias)
				}

				By("Verifying the Serve app was not redeployed")
				after, err := rayH.GetServeAppSnapshot(appName)
				Expect(err).NotTo(HaveOccurred())
				requireOrchestrationPath(after)

				// Named first, so a failure says which field moved; the whole-config
				// comparison after it is the one that also catches a field nothing
				// here knows to look at.
				for _, field := range orchestrationPathFields {
					Expect(orchestrationPathValue(after, field)).
						To(Equal(orchestrationPathValue(before, field)),
							"orchestration path field %s changed across an alias write", field)
				}

				Expect(string(after.DeployedAppConfig)).To(Equal(string(before.DeployedAppConfig)),
					"the deployed Serve config changed across an alias write")
				Expect(after.LastDeployedTimeS).To(Equal(before.LastDeployedTimeS),
					"last_deployed_time_s moved — the Serve app was redeployed")

				By("Verifying the engine containers were neither replaced nor restarted")
				containersAfter := engineContainersOnNodes(nodes, sshUser, keyFile, engineImage, registryMountPath)
				Expect(containersAfter).To(Equal(containersBefore),
					"the engine container set changed across an alias write")

				By("Verifying the controller never wrote an intermediate phase")
				epAfter := getEndpoint(epName)
				Expect(epAfter.Status.LastTransitionTime).To(Equal(lastTransitionBefore),
					"endpoint LastTransitionTime changed — before=%q after=%q",
					lastTransitionBefore, epAfter.Status.LastTransitionTime)

				By("Verifying the endpoint still serves inference")
				code, respBody, err := inferChat(epAfter.Status.ServiceURL, "Hello after an alias change")
				Expect(err).NotTo(HaveOccurred())
				Expect(code).To(Equal(http.StatusOK), "inference after an alias change failed: %s", respBody)
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

var _ = Describe("SSH Engine Package Import", Ordered,
	Label("endpoint", "ssh", "inference", "engine-import"), func() {
		var (
			clusterName        string
			clusterReady       bool
			imageRegistryOwned bool
			modelRegistryReady bool
		)

		BeforeAll(func() {
			requireEngineImportProfile()
			imageRegistryOwned = imageRegistryYAML == ""
			clusterName = setupSSHCluster("e2e-ep-ssh-import-")
			clusterReady = true
			SetupModelRegistry()
			modelRegistryReady = true
		})

		AfterAll(func() {
			if modelRegistryReady {
				TeardownModelRegistry()
			}
			if clusterReady {
				NewClusterHelper().EnsureDeleted(clusterName)
			}
			if imageRegistryOwned {
				TeardownImageRegistry()
			}
		})

		It("should import each configured engine and serve chat", Label("happy-path"), func() {
			engineH := NewProfileEngineHelper()
			clusterAccType, clusterAccProduct := getClusterAccelerator(clusterName)

			for _, engineName := range profileEngineNames() {
				func(engineName string) {
					engineVersion := profileEngineVersionFor(engineName)
					model := profileModelForEngine(engineName)
					accType := strings.ToLower(profileEngineAcceleratorType(engineName))

					By(fmt.Sprintf("Importing %s %s from the profile package directory", engineName, engineVersion))
					packageURL, err := enginePackageDownloadURL(
						profilePackageURL(), engineName, engineVersion, profilePackageArch(),
					)
					Expect(err).NotTo(HaveOccurred())

					// Import is intentionally run without --force. A pre-existing version
					// must not be overwritten by this test.
					before := engineH.Get(engineName)
					if before.ExitCode == 0 {
						for _, version := range parseEngineJSON(before.Stdout).Spec.Versions {
							if version.Version == engineVersion {
								Fail(fmt.Sprintf("engine %s version %s already exists; refusing to overwrite", engineName, engineVersion))
							}
						}
					}

					imported := false
					epName := fmt.Sprintf("e2e-ep-ssh-import-%s-%s", strings.ReplaceAll(engineName, "_", "-"), Cfg.RunID)
					endpointCreated := false
					// Register cleanup in reverse dependency order: endpoint first, then
					// the engine version that supplied its image.
					defer func() {
						if imported {
							r := engineH.RemoveVersion(engineName, engineVersion, "--force")
							ExpectSuccess(r)
						}
					}()
					defer func() {
						if endpointCreated {
							deleteEndpoint(epName)
						}
					}()

					r := engineH.ImportPackageURL(engineName, engineVersion, packageURL)
					ExpectSuccess(r)
					imported = true

					r = engineH.Get(engineName)
					ExpectSuccess(r)
					engine := parseEngineJSON(r.Stdout)
					var versionImages map[string]struct {
						ImageName string `json:"image_name"`
						Tag       string `json:"tag"`
					}
					for _, version := range engine.Spec.Versions {
						if version.Version == engineVersion {
							versionImages = version.Images
							break
						}
					}
					Expect(versionImages).NotTo(BeNil(), "imported engine version %s was not returned", engineVersion)
					Expect(versionImages).To(HaveKey(accType), "engine image for accelerator %s is missing", accType)

					options := []EndpointOption{
						withEngine(engineName, engineVersion),
						withModelProfile(model),
					}
					switch accType {
					case "cpu":
						options = append(options, withCPUOnly())
					default:
						Expect(clusterAccType).To(Equal(accType),
							"profile accelerator %s does not match SSH cluster accelerator %s", accType, clusterAccType)
						Expect(clusterAccProduct).NotTo(BeEmpty(), "SSH cluster did not report an accelerator product")
						options = append(options, withGPU("1"), withAccelerator(accType, clusterAccProduct))
					}

					By(fmt.Sprintf("Deploying %s endpoint with the shared model registry", engineName))
					yamlPath := applyEndpoint(epName, clusterName, options...)
					endpointCreated = true
					defer os.Remove(yamlPath)

					waitEndpointRunning(epName)
					ep := getEndpoint(epName)
					Expect(ep.Spec.Engine).NotTo(BeNil())
					Expect(ep.Spec.Model).NotTo(BeNil())
					Expect(ep.Spec.Engine.Engine).To(Equal(engineName))
					Expect(ep.Spec.Engine.Version).To(Equal(engineVersion))
					Expect(ep.Spec.Model.Name).To(Equal(model.Name))
					Expect(ep.Status.ServiceURL).NotTo(BeEmpty())

					code, body, err := inferChatWithModel(ep.Status.ServiceURL, model.Name, "Hello")
					Expect(err).NotTo(HaveOccurred())
					Expect(code).To(Equal(http.StatusOK), "chat completion failed for %s: %s", engineName, body)
					Expect(body).To(ContainSubstring("choices"))
				}(engineName)
			}
		})
	})
