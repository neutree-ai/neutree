package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// engineArgsYAML returns a YAML snippet for spec.variables.engine_args.
func engineArgsYAML() string {
	raw := profileEngineArgs()

	var lines []string
	for _, pair := range strings.Split(raw, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok || k == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("      %s: %s", strings.TrimSpace(k), strings.TrimSpace(v)))
	}

	return "\n" + strings.Join(lines, "\n")
}

// --- Endpoint helpers ---

// applyEndpointOnCluster renders and applies an endpoint on a specific cluster.
func applyEndpointOnCluster(name, cluster, engineVersion string) (yamlPath string) {
	defaults := map[string]string{
		"E2E_ENDPOINT_NAME":       name,
		"E2E_WORKSPACE":           profileWorkspace(),
		"E2E_CLUSTER_NAME":        cluster,
		"E2E_ENGINE_NAME":         profileEngineName(),
		"E2E_ENGINE_VERSION":      engineVersion,
		"E2E_MODEL_REGISTRY":      testRegistry(),
		"E2E_MODEL_NAME":          profileModelName(),
		"E2E_MODEL_VERSION":       profileModelVersion(),
		"E2E_MODEL_TASK":          profileModelTask(),
		"E2E_ACCELERATOR_TYPE":    profileAcceleratorType(),
		"E2E_ACCELERATOR_PRODUCT": profileAcceleratorProduct(),
		"E2E_ENGINE_ARGS_YAML":    engineArgsYAML(),
	}

	yamlPath, err := renderTemplateToTempFile(
		filepath.Join("testdata", "endpoint.yaml"), defaults,
	)
	Expect(err).NotTo(HaveOccurred(), "failed to render endpoint template")

	r := RunCLI("apply", "-f", yamlPath, "--force-update")
	ExpectSuccess(r)

	return yamlPath
}

// applyEndpoint renders the endpoint template on the default cluster and applies it.
func applyEndpoint(name, engineVersion string) (yamlPath string) {
	return applyEndpointOnCluster(name, profileEndpointCluster(), engineVersion)
}

// waitEndpointRunning waits for an endpoint to reach Running phase.
func waitEndpointRunning(name string) {
	r := RunCLI("wait", "endpoint", name,
		"-w", profileWorkspace(),
		"--for", "jsonpath=.status.phase=Running",
		"--timeout", profileEndpointTimeout(),
	)
	ExpectSuccess(r)
}

// getEndpoint retrieves endpoint details as JSON.
func getEndpoint(name string) endpointJSON {
	r := RunCLI("get", "endpoint", name, "-w", profileWorkspace(), "-o", "json")
	ExpectSuccess(r)

	return parseEndpointJSON(r.Stdout)
}

// deleteEndpoint deletes an endpoint and waits for it to be removed.
func deleteEndpoint(name string) {
	RunCLI("delete", "endpoint", name, "-w", profileWorkspace(), "--force", "--ignore-not-found")
	RunCLI("wait", "endpoint", name,
		"-w", profileWorkspace(),
		"--for", "delete",
		"--timeout", "5m",
	)
}

// --- JSON parsers ---

type endpointJSON struct {
	Spec struct {
		Cluster string `json:"cluster"`
		Engine  struct {
			Engine  string `json:"engine"`
			Version string `json:"version"`
		} `json:"engine"`
	} `json:"spec"`
	Status struct {
		Phase      string `json:"phase"`
		ServiceURL string `json:"service_url"`
	} `json:"status"`
}

func parseEndpointJSON(stdout string) endpointJSON {
	var ep endpointJSON
	ExpectWithOffset(1, json.Unmarshal([]byte(stdout), &ep)).To(Succeed())

	return ep
}

// --- Cluster pre-check helper ---

type clusterPreCheckJSON struct {
	Spec struct {
		Type    string `json:"type"`
		Version string `json:"version"`
	} `json:"spec"`
	Status struct {
		Phase           string  `json:"phase"`
		AcceleratorType *string `json:"accelerator_type"`
	} `json:"status"`
}

func parseEndpointClusterJSON(stdout string) clusterPreCheckJSON {
	var c clusterPreCheckJSON
	ExpectWithOffset(1, json.Unmarshal([]byte(stdout), &c)).To(Succeed())
	return c
}

// applyEndpointWithTask renders the endpoint template with custom model/task and applies it.
// extraEngineArgs are appended after the profile-level engine_args (e.g. "enable_prefix_caching=false").
func applyEndpointWithTask(name, engineVersion, model, modelVer, task string, extraEngineArgs ...string) (yamlPath string) {
	argsYAML := engineArgsYAML()
	for _, pair := range extraEngineArgs {
		k, v, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if ok && k != "" {
			argsYAML += fmt.Sprintf("\n      %s: %s", strings.TrimSpace(k), strings.TrimSpace(v))
		}
	}

	defaults := map[string]string{
		"E2E_ENDPOINT_NAME":       name,
		"E2E_WORKSPACE":           profileWorkspace(),
		"E2E_CLUSTER_NAME":        profileEndpointCluster(),
		"E2E_ENGINE_NAME":         profileEngineName(),
		"E2E_ENGINE_VERSION":      engineVersion,
		"E2E_MODEL_REGISTRY":      testRegistry(),
		"E2E_MODEL_NAME":          model,
		"E2E_MODEL_VERSION":       modelVer,
		"E2E_MODEL_TASK":          task,
		"E2E_ACCELERATOR_TYPE":    profileAcceleratorType(),
		"E2E_ACCELERATOR_PRODUCT": profileAcceleratorProduct(),
		"E2E_ENGINE_ARGS_YAML":    argsYAML,
	}

	yamlPath, err := renderTemplateToTempFile(
		filepath.Join("testdata", "endpoint.yaml"), defaults,
	)
	Expect(err).NotTo(HaveOccurred(), "failed to render endpoint template")

	r := RunCLI("apply", "-f", yamlPath, "--force-update")
	ExpectSuccess(r)

	return yamlPath
}

// --- Inference helpers ---

// doInferenceRequest sends a JSON POST to the given URL path and returns (status_code, body).
func doInferenceRequest(serviceURL, path string, reqBody map[string]any) (int, string) {
	payloadBytes, err := json.Marshal(reqBody)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "failed to marshal inference request")

	client := &http.Client{Timeout: 60 * time.Second}

	req, err := http.NewRequest(http.MethodPost,
		strings.TrimRight(serviceURL, "/")+path,
		strings.NewReader(string(payloadBytes)),
	)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "failed to create inference request")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+Cfg.APIKey)

	resp, err := client.Do(req)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "inference request failed")
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "failed to read inference response")

	return resp.StatusCode, string(body)
}

// inferChat sends a chat completion request and returns (status_code, body).
func inferChat(serviceURL, prompt string) (int, string) {
	return doInferenceRequest(serviceURL, "/v1/chat/completions", map[string]any{
		"model": profileModelName(),
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"max_tokens": 16,
	})
}

// inferEmbedding sends an embedding request and returns (status_code, body).
func inferEmbedding(serviceURL, model, input string) (int, string) {
	return doInferenceRequest(serviceURL, "/v1/embeddings", map[string]any{
		"model": model,
		"input": input,
	})
}

// inferRerank sends a rerank request and returns (status_code, body).
func inferRerank(serviceURL, model, query string, documents []string) (int, string) {
	return doInferenceRequest(serviceURL, "/v1/rerank", map[string]any{
		"model":     model,
		"query":     query,
		"documents": documents,
	})
}

// --- Tests ---

var _ = Describe("Endpoint", Ordered, Label("endpoint"), func() {

	BeforeAll(func() {
		if profileEndpointCluster() == "" {
			Skip("endpoint.cluster not configured in profile, skipping endpoint tests")
		}
		if profileModelName() == "" {
			Skip("Model name not configured in profile, skipping endpoint tests")
		}

		By("Verifying cluster is ready")
		r := RunCLI("get", "cluster", profileEndpointCluster(), "-w", profileWorkspace(), "-o", "json")
		ExpectSuccess(r)
		c := parseEndpointClusterJSON(r.Stdout)
		Expect(c.Status.Phase).To(Equal("Running"), "cluster must be Running")
		Expect(c.Status.AcceleratorType).NotTo(BeNil(), "cluster must have accelerator type")

		By("Setting up model registry")
		SetupModelRegistry()
	})

	AfterAll(func() {
		if profileEndpointCluster() == "" {
			return
		}

		TeardownModelRegistry()
	})

	Describe("Chat Inference", Label("chat"), func() {
		var epName string

		BeforeAll(func() {
			epName = "e2e-ep-single-" + Cfg.RunID
		})

		AfterAll(func() {
			if epName != "" {
				deleteEndpoint(epName)
			}
		})

		It("should deploy with engine container and reach Running", func() {
			yamlPath := applyEndpoint(epName, profileEngineVersionB())
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(Equal("Running"))
			Expect(ep.Spec.Engine.Version).To(Equal(profileEngineVersionB()))
			Expect(ep.Status.ServiceURL).NotTo(BeEmpty())
		})

		It("should serve inference requests", func() {
			ep := getEndpoint(epName)
			code, body := inferChat(ep.Status.ServiceURL, "Hello")
			Expect(code).To(Equal(http.StatusOK), "inference failed: %s", body)
			Expect(body).To(ContainSubstring("choices"))
		})
	})

	Describe("Multi-Version Isolation", Label("multi-version"), func() {
		var epNameA, epNameB string

		BeforeAll(func() {
			epNameA = "e2e-ep-va-" + Cfg.RunID
			epNameB = "e2e-ep-vb-" + Cfg.RunID
		})

		AfterAll(func() {
			deleteEndpoint(epNameA)
			deleteEndpoint(epNameB)
		})

		It("should run two endpoints with different engine versions", func() {
			yamlA := applyEndpoint(epNameA, profileEngineVersion())
			defer os.Remove(yamlA)
			yamlB := applyEndpoint(epNameB, profileEngineVersionB())
			defer os.Remove(yamlB)

			waitEndpointRunning(epNameA)
			waitEndpointRunning(epNameB)

			epA := getEndpoint(epNameA)
			epB := getEndpoint(epNameB)
			Expect(epA.Status.Phase).To(Equal("Running"))
			Expect(epB.Status.Phase).To(Equal("Running"))
			Expect(epA.Spec.Engine.Version).To(Equal(profileEngineVersion()))
			Expect(epB.Spec.Engine.Version).To(Equal(profileEngineVersionB()))
		})

		It("should serve inference from both endpoints", func() {
			epA := getEndpoint(epNameA)
			epB := getEndpoint(epNameB)

			codeA, bodyA := inferChat(epA.Status.ServiceURL, "Hello")
			Expect(codeA).To(Equal(http.StatusOK), "inference on ep-A failed: %s", bodyA)
			Expect(bodyA).To(ContainSubstring("choices"))

			codeB, bodyB := inferChat(epB.Status.ServiceURL, "Hello")
			Expect(codeB).To(Equal(http.StatusOK), "inference on ep-B failed: %s", bodyB)
			Expect(bodyB).To(ContainSubstring("choices"))
		})
	})

	Describe("Embedding Inference", Label("embedding"), func() {
		var epName string

		BeforeAll(func() {
			if profileEmbeddingModelName() == "" {
				Skip("embedding_model.name not configured in profile, skipping embedding tests")
			}
			epName = "e2e-ep-embed-" + Cfg.RunID
		})

		AfterAll(func() {
			if epName != "" {
				deleteEndpoint(epName)
			}
		})

		It("should deploy embedding endpoint and reach Running", func() {
			yamlPath := applyEndpointWithTask(epName, profileEngineVersionB(),
				profileEmbeddingModelName(), profileEmbeddingModelVersion(), "text-embedding",
				"enable_prefix_caching=false")
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(Equal("Running"))
			Expect(ep.Status.ServiceURL).NotTo(BeEmpty())
		})

		It("should serve embedding requests", func() {
			ep := getEndpoint(epName)
			code, body := inferEmbedding(ep.Status.ServiceURL, profileEmbeddingModelName(), "Hello world")
			Expect(code).To(Equal(http.StatusOK), "embedding inference failed: %s", body)

			var resp map[string]any
			Expect(json.Unmarshal([]byte(body), &resp)).To(Succeed())
			Expect(resp).To(HaveKey("data"))

			data, ok := resp["data"].([]any)
			Expect(ok).To(BeTrue(), "data should be an array")
			Expect(data).NotTo(BeEmpty(), "embedding data should not be empty")

			first, ok := data[0].(map[string]any)
			Expect(ok).To(BeTrue())
			Expect(first).To(HaveKey("embedding"))

			embedding, ok := first["embedding"].([]any)
			Expect(ok).To(BeTrue(), "embedding should be an array of floats")
			Expect(embedding).NotTo(BeEmpty(), "embedding vector should not be empty")
		})

		It("should serve batch embedding requests", func() {
			ep := getEndpoint(epName)
			code, body := doInferenceRequest(ep.Status.ServiceURL, "/v1/embeddings", map[string]any{
				"model": profileEmbeddingModelName(),
				"input": []string{"Hello world", "Goodbye world"},
			})
			Expect(code).To(Equal(http.StatusOK), "batch embedding inference failed: %s", body)

			var resp map[string]any
			Expect(json.Unmarshal([]byte(body), &resp)).To(Succeed())

			data, ok := resp["data"].([]any)
			Expect(ok).To(BeTrue(), "data should be an array")
			Expect(data).To(HaveLen(2), "batch embedding should return 2 results")
		})
	})

	Describe("Rerank Inference", Label("rerank"), func() {
		var epName string

		BeforeAll(func() {
			if profileRerankModelName() == "" {
				Skip("rerank_model.name not configured in profile, skipping rerank tests")
			}
			epName = "e2e-ep-rerank-" + Cfg.RunID
		})

		AfterAll(func() {
			if epName != "" {
				deleteEndpoint(epName)
			}
		})

		It("should deploy rerank endpoint and reach Running", func() {
			yamlPath := applyEndpointWithTask(epName, profileEngineVersionB(),
				profileRerankModelName(), profileRerankModelVersion(), "text-rerank",
				"enable_prefix_caching=false")
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(Equal("Running"))
			Expect(ep.Status.ServiceURL).NotTo(BeEmpty())
		})

		It("should serve rerank requests", func() {
			ep := getEndpoint(epName)
			documents := []string{
				"Paris is the capital of France.",
				"Berlin is the capital of Germany.",
				"London is the capital of the United Kingdom.",
			}
			code, body := inferRerank(ep.Status.ServiceURL, profileRerankModelName(),
				"What is the capital of France?", documents)
			Expect(code).To(Equal(http.StatusOK), "rerank inference failed: %s", body)

			var resp map[string]any
			Expect(json.Unmarshal([]byte(body), &resp)).To(Succeed())
			Expect(resp).To(HaveKey("results"))

			results, ok := resp["results"].([]any)
			Expect(ok).To(BeTrue(), "results should be an array")
			Expect(results).To(HaveLen(len(documents)), "rerank should return a result for each document")

			for _, r := range results {
				result, ok := r.(map[string]any)
				Expect(ok).To(BeTrue())
				Expect(result).To(HaveKey("index"))
				Expect(result).To(HaveKey("relevance_score"))

				score, ok := result["relevance_score"].(float64)
				Expect(ok).To(BeTrue(), "relevance_score should be a number")
				// Scores are expected in [0,1] for models with sigmoid-normalized output (e.g. BGE-Reranker).
				Expect(score).To(BeNumerically(">=", 0.0))
				Expect(score).To(BeNumerically("<=", 1.0))
			}
		})
	})
})
