package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
	return "e2e-ee-" + Cfg.RunID
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
		"E2E_WORKSPACE":         profileWorkspace(),
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
		"-w", profileWorkspace(),
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
	r := RunCLI("get", "ExternalEndpoint", testEEName(), "-w", profileWorkspace(), "-o", "json")
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
			openaioption.WithAPIKey(Cfg.APIKey),
			openaioption.WithBaseURL(serviceURL+"/v1"),
		)
		anthropicClient = anthropic.NewClient(
			anthropicoption.WithAPIKey(Cfg.APIKey),
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
		It("should reach Running phase and generate service_url", Label("C2635095", "C2642173"), func() {
			Expect(serviceURL).NotTo(BeEmpty())

			expectedPath := fmt.Sprintf("/workspace/%s/external-endpoint/%s", profileWorkspace(), testEEName())
			Expect(serviceURL).To(ContainSubstring(expectedPath))
		})
	})

	Describe("OpenAI Compatibility", Label("openai"), func() {
		It("should return exposed model names via OpenAI SDK", Label("C2635096", "C2642174"), func() {
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

		It("should route chat completion to correct upstream and rewrite model via OpenAI SDK", Label("C2635097", "C2642175"), func() {
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

		It("should return 400 for unmapped model", Label("C2635101", "C2642179"), func() {
			_, err := oaiClient.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{
				Model: "nonexistent-model",
				Messages: []openai.ChatCompletionMessageParamUnion{
					openai.UserMessage("hello"),
				},
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("400"))
		})

		It("should forward configured Authorization header to upstream", Label("C2635100", "C2642178"), func() {
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
		It("should return model list via Anthropic SDK", Label("C2635099", "C2642177"), func() {
			page, err := anthropicClient.Models.List(context.Background(), anthropic.ModelListParams{})
			Expect(err).NotTo(HaveOccurred())

			modelIDs := make([]string, 0, len(page.Data))
			for _, m := range page.Data {
				modelIDs = append(modelIDs, m.ID)
			}

			Expect(modelIDs).To(ContainElements("fast", "smart"))
		})

		It("should handle non-stream Messages via Anthropic SDK", Label("C2635098", "C2642176"), func() {
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

		It("should handle stream Messages via Anthropic SDK", Label("C2635189", "C2642180"), func() {
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

	Describe("Credential Masking", Label("credential"), func() {
		It("should mask credential in CLI JSON output", Label("C2642210"), func() {
			By("Verifying GET single resource masks credential")
			r := RunCLI("get", "ExternalEndpoint", testEEName(), "-w", profileWorkspace(), "-o", "json")
			ExpectSuccess(r)

			Expect(r.Stdout).NotTo(ContainSubstring(mockAuthToken),
				"raw credential token must not appear in GET JSON output")

			var ee map[string]any
			Expect(json.Unmarshal([]byte(r.Stdout), &ee)).To(Succeed())

			spec, ok := ee["spec"].(map[string]any)
			Expect(ok).To(BeTrue(), "missing spec in EE response")

			upstreams, ok := spec["upstreams"].([]any)
			Expect(ok).To(BeTrue(), "missing upstreams in spec")
			Expect(upstreams).NotTo(BeEmpty())

			for _, u := range upstreams {
				upstream, ok := u.(map[string]any)
				Expect(ok).To(BeTrue())
				auth, hasAuth := upstream["auth"].(map[string]any)
				if !hasAuth {
					continue
				}
				cred, hasCred := auth["credential"].(string)
				if !hasCred {
					continue
				}
				Expect(cred).To(ContainSubstring("***"),
					"credential field should be masked with ***")
			}

			By("Verifying LIST also masks credential")
			r = RunCLI("get", "ExternalEndpoint", "-w", profileWorkspace(), "-o", "json")
			ExpectSuccess(r)
			Expect(r.Stdout).NotTo(ContainSubstring(mockAuthToken),
				"raw credential token must not appear in LIST JSON output")

			// Parse LIST response and verify credential masking in each item
			var listItems []map[string]any
			if json.Unmarshal([]byte(r.Stdout), &listItems) == nil {
				for _, item := range listItems {
					spec, _ := item["spec"].(map[string]any)
					if spec == nil {
						continue
					}

					upstreams, _ := spec["upstreams"].([]any)

					for _, u := range upstreams {
						upstream, _ := u.(map[string]any)
						if upstream == nil {
							continue
						}

						auth, _ := upstream["auth"].(map[string]any)
						if auth == nil {
							continue
						}

						if cred, ok := auth["credential"].(string); ok {
							Expect(cred).NotTo(Equal(mockAuthToken),
								"LIST response should not contain raw credential")
						}
					}
				}
			}
		})

		// C2644058 is a UI export case (YAML export dialog), not a CLI case.
		// This test verifies CLI `get -o yaml` masking independently.
		It("should strip credential from CLI YAML output", func() {
			r := RunCLI("get", "ExternalEndpoint", testEEName(), "-w", profileWorkspace(), "-o", "yaml")
			ExpectSuccess(r)

			Expect(r.Stdout).NotTo(ContainSubstring(mockAuthToken),
				"raw credential token must not appear in YAML output")

			Expect(r.Stdout).NotTo(ContainSubstring("credential"),
				"credential field should not exist in YAML output")
		})
	})

	Describe("CLI Lifecycle", Label("lifecycle"), func() {
		It("should retrieve ExternalEndpoint via CLI get", Label("C2642212"), func() {
			r := RunCLI("get", "ExternalEndpoint", testEEName(), "-w", profileWorkspace(), "-o", "json")
			ExpectSuccess(r)

			var ee map[string]any
			Expect(json.Unmarshal([]byte(r.Stdout), &ee)).To(Succeed())

			metadata, ok := ee["metadata"].(map[string]any)
			Expect(ok).To(BeTrue(), "missing metadata field in EE response")

			name, ok := metadata["name"].(string)
			Expect(ok).To(BeTrue(), "missing metadata.name field in EE response")
			Expect(name).To(Equal(testEEName()),
				"returned name should match the created ExternalEndpoint")
		})
	})

	Describe("Credentials API", Label("credentials-api"), func() {
		It("should return credential for admin via credentials endpoint", Label("C2644056"), func() {
			url := fmt.Sprintf("%s/api/v1/credentials/external_endpoints?workspace=%s&name=%s",
				strings.TrimRight(Cfg.ServerURL, "/"),
				profileWorkspace(),
				testEEName(),
			)

			client := &http.Client{Timeout: 30 * time.Second}
			req, err := http.NewRequest(http.MethodGet, url, nil)
			Expect(err).NotTo(HaveOccurred())
			req.Header.Set("Authorization", bearerAPIKey())

			resp, err := client.Do(req)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()

			body, err := io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())

			Expect(resp.StatusCode).To(Equal(http.StatusOK),
				"credentials endpoint should return 200, got body: %s", string(body))

			// The credentials API should return the actual credential for authorized users
			Expect(string(body)).To(ContainSubstring(mockAuthToken),
				"admin credentials endpoint should return the actual credential")
		})

		It("should return 403 for user without read-credentials permission", Label("C2644057"), func() {
			testUserName := "e2e-nocred-" + Cfg.RunID
			testEmail := testUserName + "@e2e-test.local"
			testPassword := "E2eTest!Pass123"
			roleName := "e2e-role-nocred-" + Cfg.RunID

			By("Creating a test user via admin API")
			userID := createTestUser(testUserName, testEmail, testPassword)
			Expect(userID).NotTo(BeEmpty(), "user creation should return a user ID")

			DeferCleanup(func() {
				deleteTestUser(userID)
				RunCLI("delete", "roleassignment", testUserName+"-ra",
					"--force", "--ignore-not-found")
				RunCLI("delete", "role", roleName,
					"-w", profileWorkspace(),
					"--force", "--ignore-not-found")
			})

			By("Creating a role WITHOUT external_endpoint:read-credentials")
			roleYAML := fmt.Sprintf(`apiVersion: v1
kind: Role
metadata:
  name: %s
  workspace: %s
spec:
  permissions:
    - "external_endpoint:read"
    - "cluster:read"
`, roleName, profileWorkspace())

			tmpFile, err := os.CreateTemp("", "e2e-role-*.yaml")
			Expect(err).NotTo(HaveOccurred())
			_, err = tmpFile.WriteString(roleYAML)
			Expect(err).NotTo(HaveOccurred())
			tmpFile.Close()
			defer os.Remove(tmpFile.Name())

			r := RunCLI("apply", "-f", tmpFile.Name())
			ExpectSuccess(r)

			By("Assigning role to user")
			raYAML := fmt.Sprintf(`apiVersion: v1
kind: RoleAssignment
metadata:
  name: %s
spec:
  user_id: "%s"
  role: "%s"
  workspace: "%s"
`, testUserName+"-ra", userID, roleName, profileWorkspace())

			tmpFile2, err := os.CreateTemp("", "e2e-ra-*.yaml")
			Expect(err).NotTo(HaveOccurred())
			_, err = tmpFile2.WriteString(raYAML)
			Expect(err).NotTo(HaveOccurred())
			tmpFile2.Close()
			defer os.Remove(tmpFile2.Name())

			r = RunCLI("apply", "-f", tmpFile2.Name())
			ExpectSuccess(r)

			By("Logging in as the test user to get JWT")
			jwt := loginTestUser(testEmail, testPassword)
			Expect(jwt).NotTo(BeEmpty(), "login should return an access token")

			By("Calling credentials API with non-admin JWT")
			credURL := fmt.Sprintf("%s/api/v1/credentials/external_endpoints?workspace=%s&name=%s",
				strings.TrimRight(Cfg.ServerURL, "/"),
				profileWorkspace(),
				testEEName(),
			)

			client := &http.Client{Timeout: 30 * time.Second}
			req, err := http.NewRequest(http.MethodGet, credURL, nil)
			Expect(err).NotTo(HaveOccurred())
			req.Header.Set("Authorization", "Bearer "+jwt)

			resp, err := client.Do(req)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()

			body, err := io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())

			Expect(resp.StatusCode).To(Equal(http.StatusForbidden),
				"credentials endpoint should return 403 for user without read-credentials permission, got body: %s",
				string(body))
		})
	})
})
