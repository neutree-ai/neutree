package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	anthropicoption "github.com/anthropics/anthropic-sdk-go/option"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/openai/openai-go"
	openaioption "github.com/openai/openai-go/option"
)

// --- ExternalEndpoint setup / teardown ---

var (
	mockUpstream *MockUpstream
	eeYAML       string
)

func testEEName() string {
	return "e2e-ee-" + runID
}

const mockAuthToken = "e2e-test-token-secret"

// SetupMockUpstream starts the mock OpenAI-compatible server.
func SetupMockUpstream() {
	mockUpstream = StartMockUpstream()
}

// TeardownMockUpstream stops the mock server.
func TeardownMockUpstream() {
	if mockUpstream != nil {
		mockUpstream.Stop()
	}
}

// SetupExternalEndpoint creates an ExternalEndpoint from the YAML template.
func SetupExternalEndpoint() {
	defaults := map[string]string{
		"E2E_EE_NAME":           testEEName(),
		"E2E_WORKSPACE":         testWorkspace(),
		"E2E_MOCK_UPSTREAM_URL": mockUpstream.ExternalURL(),
		"E2E_MOCK_AUTH_TOKEN":   mockAuthToken,
	}

	var err error
	eeYAML, err = renderTemplateToTempFile(
		filepath.Join("testdata", "external-endpoint.yaml"), defaults,
	)
	Expect(err).NotTo(HaveOccurred(), "failed to render external endpoint template")

	r := RunCLI("apply", "-f", eeYAML)
	ExpectSuccess(r)

	r = RunCLI("wait", "ExternalEndpoint", testEEName(),
		"-w", testWorkspace(),
		"--for", "jsonpath=.status.phase=Running",
		"--timeout", "2m",
	)
	ExpectSuccess(r)
}

// TeardownExternalEndpoint deletes the ExternalEndpoint and cleans up.
func TeardownExternalEndpoint() {
	if eeYAML != "" {
		RunCLI("delete", "-f", eeYAML, "--force", "--ignore-not-found")
		os.Remove(eeYAML)
	}
}

// --- Helpers ---

// getEEServiceURL retrieves the service_url from ExternalEndpoint status via CLI.
func getEEServiceURL() string {
	r := RunCLI("get", "ExternalEndpoint", testEEName(), "-w", testWorkspace(), "-o", "json")
	ExpectSuccess(r)

	var ee map[string]any
	Expect(json.Unmarshal([]byte(r.Stdout), &ee)).To(Succeed())

	status, ok := ee["status"].(map[string]any)
	Expect(ok).To(BeTrue(), "missing status in EE response")

	serviceURL, ok := status["service_url"].(string)
	Expect(ok).To(BeTrue(), "missing service_url in EE status")
	Expect(serviceURL).NotTo(BeEmpty())

	return serviceURL
}

// waitForUpstreamRequest waits for the mock upstream to receive a request,
// then returns the last request's body parsed as JSON.
func waitForUpstreamRequest() (last *RecordedRequest, body map[string]any) {
	Eventually(func() *RecordedRequest {
		return mockUpstream.LastRequest()
	}, 5*time.Second).ShouldNot(BeNil())

	last = mockUpstream.LastRequest()

	body = make(map[string]any)
	Expect(json.Unmarshal([]byte(last.Body), &body)).To(Succeed())

	return last, body
}

// --- Tests ---

var _ = Describe("ExternalEndpoint", Ordered, Label("external-endpoint"), func() {
	var (
		serviceURL     string
		oaiClient      openai.Client
		anthropicClient anthropic.Client
	)

	BeforeAll(func() {
		SetupMockUpstream()
		SetupExternalEndpoint()

		serviceURL = getEEServiceURL()
		oaiClient = openai.NewClient(
			openaioption.WithAPIKey(os.Getenv("NEUTREE_API_KEY")),
			openaioption.WithBaseURL(serviceURL+"/v1"),
		)
		anthropicClient = anthropic.NewClient(
			anthropicoption.WithAPIKey(os.Getenv("NEUTREE_API_KEY")),
			// SDK sends to {baseURL}/v1/messages; our gateway path is .../anthropic/v1/messages
			anthropicoption.WithBaseURL(serviceURL+"/anthropic"),
		)

		// Wait for Kong route to become reachable after EE reaches Running
		Eventually(func() error {
			_, err := oaiClient.Models.List(context.Background())
			return err
		}, 30*time.Second, 2*time.Second).Should(Succeed(), "gateway route not reachable")
	})

	AfterAll(func() {
		TeardownExternalEndpoint()
		TeardownMockUpstream()
	})

	Describe("Create", Label("create"), func() {
		It("should reach Running phase and generate service_url", Label("C2635095"), func() {
			Expect(serviceURL).NotTo(BeEmpty())

			expectedPath := fmt.Sprintf("/workspace/%s/external-endpoint/%s", testWorkspace(), testEEName())
			Expect(serviceURL).To(ContainSubstring(expectedPath))
		})
	})

	Describe("OpenAI Compatibility", Label("openai"), func() {
		It("should return exposed model names via OpenAI SDK", Label("C2635096"), func() {
			page, err := oaiClient.Models.List(context.Background())
			Expect(err).NotTo(HaveOccurred())

			modelIDs := make([]string, 0, len(page.Data))
			for _, m := range page.Data {
				modelIDs = append(modelIDs, m.ID)
			}

			Expect(modelIDs).To(ContainElements("fast", "smart"))
			Expect(modelIDs).NotTo(ContainElement("gpt-4o-mini"))
			Expect(modelIDs).NotTo(ContainElement("gpt-4o"))
		})

		It("should route chat completion to correct upstream and rewrite model via OpenAI SDK", Label("C2635097"), func() {
			mockUpstream.ClearRequests()

			completion, err := oaiClient.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{
				Model: "fast",
				Messages: []openai.ChatCompletionMessageParamUnion{
					openai.UserMessage("hello"),
				},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(len(completion.Choices)).To(BeNumerically(">", 0))
			Expect(completion.Choices[0].Message.Content).NotTo(BeEmpty())

			last, upstreamReq := waitForUpstreamRequest()
			Expect(last.Path).To(Equal("/v1/chat/completions"))
			Expect(upstreamReq["model"]).To(Equal("gpt-4o-mini"),
				"upstream should receive the real model name, not the exposed name")
		})

		It("should return 400 for unmapped model", Label("C2635101"), func() {
			_, err := oaiClient.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{
				Model: "nonexistent-model",
				Messages: []openai.ChatCompletionMessageParamUnion{
					openai.UserMessage("hello"),
				},
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("400"))
		})

		It("should forward configured Authorization header to upstream", Label("C2635100"), func() {
			mockUpstream.ClearRequests()

			_, err := oaiClient.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{
				Model: "fast",
				Messages: []openai.ChatCompletionMessageParamUnion{
					openai.UserMessage("test auth"),
				},
			})
			Expect(err).NotTo(HaveOccurred())

			last, _ := waitForUpstreamRequest()
			Expect(last.Headers.Get("Authorization")).To(Equal("Bearer "+mockAuthToken),
				"upstream should receive the configured auth header")
		})
	})

	Describe("Anthropic Compatibility", Label("anthropic"), func() {
		It("should return model list via Anthropic SDK", Label("C2635099"), func() {
			page, err := anthropicClient.Models.List(context.Background(), anthropic.ModelListParams{})
			Expect(err).NotTo(HaveOccurred())

			modelIDs := make([]string, 0, len(page.Data))
			for _, m := range page.Data {
				modelIDs = append(modelIDs, m.ID)
			}

			Expect(modelIDs).To(ContainElements("fast", "smart"))
		})

		It("should handle non-stream Messages via Anthropic SDK", Label("C2635098"), func() {
			mockUpstream.ClearRequests()

			msg, err := anthropicClient.Messages.New(context.Background(), anthropic.MessageNewParams{
				Model:     "fast",
				MaxTokens: 100,
				Messages: []anthropic.MessageParam{
					anthropic.NewUserMessage(anthropic.NewTextBlock("hello")),
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// SDK successfully deserialized the response — proves format compatibility
			Expect(string(msg.Role)).To(Equal("assistant"))
			Expect(string(msg.Type)).To(Equal("message"))
			Expect(len(msg.Content)).To(BeNumerically(">", 0))
			Expect(msg.StopReason).To(Equal(anthropic.StopReasonEndTurn))
			Expect(msg.Usage.InputTokens).To(BeNumerically(">", 0))

			// Verify upstream received OpenAI-format request
			last, upstreamReq := waitForUpstreamRequest()
			Expect(last.Path).To(Equal("/v1/chat/completions"))
			Expect(upstreamReq["model"]).To(Equal("gpt-4o-mini"))

			messages, ok := upstreamReq["messages"].([]any)
			Expect(ok).To(BeTrue())
			Expect(len(messages)).To(BeNumerically(">", 0))
		})

		It("should handle stream Messages via Anthropic SDK", Label("C2635189"), func() {
			mockUpstream.ClearRequests()

			stream := anthropicClient.Messages.NewStreaming(context.Background(), anthropic.MessageNewParams{
				Model:     "smart",
				MaxTokens: 100,
				Messages: []anthropic.MessageParam{
					anthropic.NewUserMessage(anthropic.NewTextBlock("hello stream")),
				},
			})
			defer stream.Close()

			var message anthropic.Message
			for stream.Next() {
				event := stream.Current()
				err := message.Accumulate(event)
				Expect(err).NotTo(HaveOccurred())
			}
			Expect(stream.Err()).NotTo(HaveOccurred())

			// SDK successfully consumed the SSE stream — proves format compatibility
			Expect(string(message.Role)).To(Equal("assistant"))
			Expect(len(message.Content)).To(BeNumerically(">", 0))

			// Verify upstream received OpenAI-format stream request
			last, upstreamReq := waitForUpstreamRequest()
			Expect(last.Path).To(Equal("/v1/chat/completions"))
			Expect(upstreamReq["model"]).To(Equal("gpt-4o"))
			Expect(upstreamReq["stream"]).To(BeTrue())
		})
	})
})
