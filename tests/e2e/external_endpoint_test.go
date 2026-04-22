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

	v1 "github.com/neutree-ai/neutree/api/v1"
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
		serviceURL      string
		oaiClient       openai.Client
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
		It("should reach Running phase and generate service_url", Label("C2642173"), func() {
			Expect(serviceURL).NotTo(BeEmpty())

			expectedPath := fmt.Sprintf("/workspace/%s/external-endpoint/%s", profileWorkspace(), testEEName())
			Expect(serviceURL).To(ContainSubstring(expectedPath))
		})
	})

	Describe("OpenAI Compatibility", Label("openai"), func() {
		It("should return exposed model names via OpenAI SDK", Label("C2642174"), func() {
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

		It("should route chat completion to correct upstream and rewrite model via OpenAI SDK", Label("C2642175"), func() {
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

		It("should return 400 for unmapped model", Label("C2642179"), func() {
			_, err := oaiClient.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{
				Model: "nonexistent-model",
				Messages: []openai.ChatCompletionMessageParamUnion{
					openai.UserMessage("hello"),
				},
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("400"))
		})

		It("should forward configured Authorization header to upstream", Label("C2642178"), func() {
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
		It("should return model list via Anthropic SDK", Label("C2642177"), func() {
			page, err := anthropicClient.Models.List(context.Background(), anthropic.ModelListParams{})
			Expect(err).NotTo(HaveOccurred())

			modelIDs := make([]string, 0, len(page.Data))
			for _, m := range page.Data {
				modelIDs = append(modelIDs, m.ID)
			}

			Expect(modelIDs).To(ContainElements("fast", "smart"))
		})

		It("should handle non-stream Messages via Anthropic SDK", Label("C2642176"), func() {
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

		It("should handle stream Messages via Anthropic SDK", Label("C2642180"), func() {
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

	// Credential masking is implemented by stripping the credential field entirely
	// from API responses (not masking with ***). Both GET and LIST should not return
	// the credential field at all.
	Describe("Credential Masking", Label("credential"), func() {
		It("should strip credential from CLI GET/LIST output", Label("C2642210"), func() {
			By("Verifying GET JSON strips credential")
			r := RunCLI("get", "ExternalEndpoint", testEEName(), "-w", profileWorkspace(), "-o", "json")
			ExpectSuccess(r)

			Expect(r.Stdout).NotTo(ContainSubstring(mockAuthToken),
				"raw credential token must not appear in GET JSON output")
			Expect(r.Stdout).NotTo(ContainSubstring("credential"),
				"credential field should not exist in GET JSON output")

			By("Verifying LIST JSON strips credential")
			r = RunCLI("get", "ExternalEndpoint", "-w", profileWorkspace(), "-o", "json")
			ExpectSuccess(r)

			Expect(r.Stdout).NotTo(ContainSubstring(mockAuthToken),
				"raw credential token must not appear in LIST JSON output")
			Expect(r.Stdout).NotTo(ContainSubstring("credential"),
				"credential field should not exist in LIST JSON output")

			By("Verifying GET YAML strips credential")
			r = RunCLI("get", "ExternalEndpoint", testEEName(), "-w", profileWorkspace(), "-o", "yaml")
			ExpectSuccess(r)

			Expect(r.Stdout).NotTo(ContainSubstring(mockAuthToken),
				"raw credential token must not appear in YAML output")
			Expect(r.Stdout).NotTo(ContainSubstring("credential"),
				"credential field should not exist in YAML output")
		})
	})

	Describe("CLI Lifecycle", Label("lifecycle"), func() {
		It("should apply, get, and delete ExternalEndpoint via CLI", Label("C2642212"), func() {
			eeName := "e2e-ee-lifecycle-" + Cfg.RunID

			By("Applying ExternalEndpoint via CLI")
			eeYAML := fmt.Sprintf(`apiVersion: v1
kind: ExternalEndpoint
metadata:
  name: %s
  workspace: %s
spec:
  upstreams:
    - upstream:
        url: http://example.com
      model_mapping:
        test-model: test-model
`, eeName, profileWorkspace())

			tmpFile, err := os.CreateTemp("", "e2e-ee-lifecycle-*.yaml")
			Expect(err).NotTo(HaveOccurred())
			_, err = tmpFile.WriteString(eeYAML)
			Expect(err).NotTo(HaveOccurred())
			tmpFile.Close()
			defer os.Remove(tmpFile.Name())

			r := RunCLI("apply", "-f", tmpFile.Name())
			ExpectSuccess(r)
			Expect(r.Stdout).To(ContainSubstring("created"))

			By("Getting ExternalEndpoint via CLI and verifying fields")
			r = RunCLI("get", "ExternalEndpoint", eeName, "-w", profileWorkspace(), "-o", "json")
			ExpectSuccess(r)

			var ee v1.ExternalEndpoint
			Expect(json.Unmarshal([]byte(r.Stdout), &ee)).To(Succeed())
			Expect(ee.Metadata).NotTo(BeNil())
			Expect(ee.Metadata.Name).To(Equal(eeName))
			Expect(ee.Metadata.Workspace).To(Equal(profileWorkspace()))

			By("Deleting ExternalEndpoint via CLI")
			r = RunCLI("delete", "ExternalEndpoint", eeName, "-w", profileWorkspace(), "--force")
			ExpectSuccess(r)

			By("Verifying ExternalEndpoint is deleted")
			r = RunCLI("get", "ExternalEndpoint", eeName, "-w", profileWorkspace())
			ExpectFailed(r)
		})
	})

	Describe("Credentials API", Label("credentials-api"), func() {
		It("should return credential for admin via credentials endpoint", Label("C2644056"), func() {
			By("Logging in as admin to get JWT")
			jwt := loginTestUser(profile.Auth.Email, profile.Auth.Password)
			Expect(jwt).NotTo(BeEmpty())

			url := fmt.Sprintf("%s/api/v1/credentials/external_endpoints?metadata->>workspace=eq.%s&metadata->>name=eq.%s",
				strings.TrimRight(Cfg.ServerURL, "/"),
				profileWorkspace(),
				testEEName(),
			)

			client := &http.Client{Timeout: 30 * time.Second}
			req, err := http.NewRequest(http.MethodGet, url, nil)
			Expect(err).NotTo(HaveOccurred())
			req.Header.Set("Authorization", "Bearer "+jwt)

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
			var userID string

			By("Logging in as admin to get JWT for user management")
			adminJWT := loginTestUser(profile.Auth.Email, profile.Auth.Password)
			Expect(adminJWT).NotTo(BeEmpty())

			DeferCleanup(func() {
				if userID != "" {
					deleteTestUser(adminJWT, userID)
				}

				RunCLI("delete", "roleassignment", testUserName+"-ra",
					"-w", profileWorkspace(), "--force", "--ignore-not-found")
				RunCLI("delete", "role", roleName,
					"-w", profileWorkspace(),
					"--force", "--ignore-not-found")
			})

			By("Creating a test user via admin API")
			userID = createTestUser(adminJWT, testUserName, testEmail, testPassword)
			Expect(userID).NotTo(BeEmpty(), "user creation should return a user ID")

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
			credURL := fmt.Sprintf("%s/api/v1/credentials/external_endpoints?metadata->>workspace=eq.%s&metadata->>name=eq.%s",
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
