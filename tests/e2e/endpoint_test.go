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

// --- Environment helpers ---

func clusterName() string     { return os.Getenv("E2E_CLUSTER_NAME") }
func engineName() string          { return envOrDefault("E2E_ENGINE_NAME", "vllm") }
func engineVersionA() string      { return envOrDefault("E2E_ENGINE_VERSION_A", "v0.8.5") }
func engineVersionB() string      { return envOrDefault("E2E_ENGINE_VERSION_B", "v0.11.2") }
func modelName() string           { return os.Getenv("E2E_MODEL_NAME") }
func modelVersion() string        { return envOrDefault("E2E_MODEL_VERSION", "latest") }
func modelTask() string           { return envOrDefault("E2E_MODEL_TASK", "text-generation") }
func acceleratorType() string     { return envOrDefault("E2E_ACCELERATOR_TYPE", "nvidia_gpu") }
func acceleratorProduct() string  { return envOrDefault("E2E_ACCELERATOR_PRODUCT", "") }
func endpointTimeout() string     { return envOrDefault("E2E_ENDPOINT_TIMEOUT", "10m") }

// engineArgsYAML parses E2E_ENDPOINT_ENGINE_ARGS (comma-separated key=value pairs,
// default "dtype=half") and returns a YAML snippet for spec.variables.engine_args.
func engineArgsYAML() string {
	raw := envOrDefault("E2E_ENDPOINT_ENGINE_ARGS", "dtype=half")
	if raw == "" {
		return ""
	}

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

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return fallback
}

// --- Endpoint helpers ---

// applyEndpoint renders the endpoint template and applies it.
func applyEndpoint(name, engineVersion string) (yamlPath string) {
	defaults := map[string]string{
		"E2E_ENDPOINT_NAME":       name,
		"E2E_WORKSPACE":           testWorkspace(),
		"E2E_CLUSTER_NAME":    clusterName(),
		"E2E_ENGINE_NAME":         engineName(),
		"E2E_ENGINE_VERSION":      engineVersion,
		"E2E_MODEL_REGISTRY":      testRegistry(),
		"E2E_MODEL_NAME":          modelName(),
		"E2E_MODEL_VERSION":       modelVersion(),
		"E2E_MODEL_TASK":          modelTask(),
		"E2E_ACCELERATOR_TYPE":    acceleratorType(),
		"E2E_ACCELERATOR_PRODUCT": acceleratorProduct(),
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

// waitEndpointRunning waits for an endpoint to reach Running phase.
func waitEndpointRunning(name string) {
	r := RunCLI("wait", "endpoint", name,
		"-w", testWorkspace(),
		"--for", "jsonpath=.status.phase=Running",
		"--timeout", endpointTimeout(),
	)
	ExpectSuccess(r)
}

// getEndpoint retrieves endpoint details as JSON.
func getEndpoint(name string) endpointJSON {
	r := RunCLI("get", "endpoint", name, "-w", testWorkspace(), "-o", "json")
	ExpectSuccess(r)

	return parseEndpointJSON(r.Stdout)
}

// deleteEndpoint deletes an endpoint and waits for it to be removed.
func deleteEndpoint(name string) {
	RunCLI("delete", "endpoint", name, "-w", testWorkspace(), "--force", "--ignore-not-found")
	RunCLI("wait", "endpoint", name,
		"-w", testWorkspace(),
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

// --- Inference helper ---

// inferChat sends a chat completion request and returns (status_code, body).
func inferChat(serviceURL, prompt string) (int, string) {
	reqBody := map[string]any{
		"model": modelName(),
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"max_tokens": 16,
	}
	payloadBytes, err := json.Marshal(reqBody)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "failed to marshal chat request")
	payload := string(payloadBytes)

	client := &http.Client{Timeout: 60 * time.Second}

	req, err := http.NewRequest(http.MethodPost,
		strings.TrimRight(serviceURL, "/")+"/v1/chat/completions",
		strings.NewReader(payload),
	)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "failed to create inference request")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+os.Getenv("NEUTREE_API_KEY"))

	resp, err := client.Do(req)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "inference request failed")
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "failed to read inference response")

	return resp.StatusCode, string(body)
}

// --- Tests ---

var _ = Describe("Endpoint", Ordered, Label("endpoint"), func() {

	BeforeAll(func() {
		if clusterName() == "" {
			Skip("E2E_CLUSTER_NAME not set, skipping endpoint tests")
		}
		if modelName() == "" {
			Skip("E2E_MODEL_NAME not set, skipping endpoint tests")
		}

		By("Verifying cluster is ready")
		r := RunCLI("get", "cluster", clusterName(), "-w", testWorkspace(), "-o", "json")
		ExpectSuccess(r)
		c := parseEndpointClusterJSON(r.Stdout)
		Expect(c.Status.Phase).To(Equal("Running"), "cluster must be Running")
		Expect(c.Status.AcceleratorType).NotTo(BeNil(), "cluster must have accelerator type")

		By("Setting up model registry")
		SetupModelRegistry()
	})

	AfterAll(func() {
		if clusterName() == "" {
			return
		}

		TeardownModelRegistry()
	})

	Describe("Deploy and Inference", Label("deploy"), func() {
		var epName string

		BeforeAll(func() {
			epName = "e2e-ep-single-" + runID
		})

		AfterAll(func() {
			deleteEndpoint(epName)
		})

		It("should deploy with engine container and reach Running", func() {
			yamlPath := applyEndpoint(epName, engineVersionB())
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)

			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(Equal("Running"))
			Expect(ep.Spec.Engine.Version).To(Equal(engineVersionB()))
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
			epNameA = "e2e-ep-va-" + runID
			epNameB = "e2e-ep-vb-" + runID
		})

		AfterAll(func() {
			deleteEndpoint(epNameA)
			deleteEndpoint(epNameB)
		})

		It("should run two endpoints with different engine versions", func() {
			yamlA := applyEndpoint(epNameA, engineVersionA())
			defer os.Remove(yamlA)
			yamlB := applyEndpoint(epNameB, engineVersionB())
			defer os.Remove(yamlB)

			waitEndpointRunning(epNameA)
			waitEndpointRunning(epNameB)

			epA := getEndpoint(epNameA)
			epB := getEndpoint(epNameB)
			Expect(epA.Status.Phase).To(Equal("Running"))
			Expect(epB.Status.Phase).To(Equal("Running"))
			Expect(epA.Spec.Engine.Version).To(Equal(engineVersionA()))
			Expect(epB.Spec.Engine.Version).To(Equal(engineVersionB()))
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
})
