package framework

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// Client is a wrapper around the Neutree API.
type Client struct {
	httpClient  *http.Client
	apiEndpoint string
	token       string
}

// NewClient creates a new Neutree API client.
func NewClient(apiEndpoint string) *Client {
	return &Client{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		apiEndpoint: strings.TrimSuffix(apiEndpoint, "/"),
	}
}

// SetToken sets the authentication token for API requests.
func (c *Client) SetToken(token string) {
	c.token = token
}

// Login authenticates with the API and returns an access token.
func (c *Client) Login(email, password string) (string, error) {
	// GoTrue authentication endpoint
	// Construct URL by appending to base endpoint
	authURL := strings.TrimSuffix(c.apiEndpoint, "/") + "/auth/token?grant_type=password"

	payload := map[string]string{
		"email":    email,
		"password": password,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal login payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, authURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("failed to create login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send login request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("login failed with status %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to decode login response: %w", err)
	}

	return result.AccessToken, nil
}

// doRequest performs an HTTP request with authentication.
func (c *Client) doRequest(method, path string, body any) (*http.Response, error) {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, c.apiEndpoint+path, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if method == http.MethodPost || method == http.MethodPatch {
		req.Header.Set("Prefer", "return=representation")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	return c.httpClient.Do(req)
}

// CreateImageRegistry creates a new image registry.
func (c *Client) CreateImageRegistry(ir *v1.ImageRegistry) (*v1.ImageRegistry, error) {
	ir.APIVersion = "v1"
	ir.Kind = "ImageRegistry"

	resp, err := c.doRequest(http.MethodPost, "/image_registries", ir)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to create image registry: status %d, body: %s", resp.StatusCode, string(body))
	}

	var results []v1.ImageRegistry
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if len(results) == 0 {
		return nil, fmt.Errorf("empty response when creating image registry")
	}

	return &results[0], nil
}

// GetImageRegistry retrieves an image registry by workspace and name.
func (c *Client) GetImageRegistry(workspace, name string) (*v1.ImageRegistry, error) {
	path := fmt.Sprintf("/image_registries?metadata->>workspace=eq.%s&metadata->>name=eq.%s",
		url.QueryEscape(workspace), url.QueryEscape(name))

	resp, err := c.doRequest(http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to get image registry: status %d, body: %s", resp.StatusCode, string(body))
	}

	var results []v1.ImageRegistry
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if len(results) == 0 {
		return nil, fmt.Errorf("image registry not found: %s/%s", workspace, name)
	}

	return &results[0], nil
}

// DeleteImageRegistry soft-deletes an image registry.
func (c *Client) DeleteImageRegistry(workspace, name string) error {
	ir, err := c.GetImageRegistry(workspace, name)
	if err != nil {
		return err
	}

	ir.Metadata.DeletionTimestamp = time.Now().Format(time.RFC3339)

	path := fmt.Sprintf("/image_registries?id=eq.%d", ir.ID)
	resp, err := c.doRequest(http.MethodPatch, path, ir)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to delete image registry: status %d, body: %s", resp.StatusCode, string(body))
	}

	return nil
}

// CreateCluster creates a new cluster.
func (c *Client) CreateCluster(cluster *v1.Cluster) (*v1.Cluster, error) {
	cluster.APIVersion = "v1"
	cluster.Kind = "Cluster"

	resp, err := c.doRequest(http.MethodPost, "/clusters", cluster)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to create cluster: status %d, body: %s", resp.StatusCode, string(body))
	}

	var results []v1.Cluster
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if len(results) == 0 {
		return nil, fmt.Errorf("empty response when creating cluster")
	}

	return &results[0], nil
}

// GetCluster retrieves a cluster by workspace and name.
func (c *Client) GetCluster(workspace, name string) (*v1.Cluster, error) {
	path := fmt.Sprintf("/clusters?metadata->>workspace=eq.%s&metadata->>name=eq.%s",
		url.QueryEscape(workspace), url.QueryEscape(name))

	resp, err := c.doRequest(http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to get cluster: status %d, body: %s", resp.StatusCode, string(body))
	}

	var results []v1.Cluster
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if len(results) == 0 {
		return nil, fmt.Errorf("cluster not found: %s/%s", workspace, name)
	}

	return &results[0], nil
}

// DeleteCluster soft-deletes a cluster.
func (c *Client) DeleteCluster(workspace, name string) error {
	cluster, err := c.GetCluster(workspace, name)
	if err != nil {
		return err
	}

	cluster.Metadata.DeletionTimestamp = time.Now().Format(time.RFC3339)

	path := fmt.Sprintf("/clusters?id=eq.%d", cluster.ID)
	resp, err := c.doRequest(http.MethodPatch, path, cluster)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to delete cluster: status %d, body: %s", resp.StatusCode, string(body))
	}

	return nil
}

// CreateModelRegistry creates a new model registry.
func (c *Client) CreateModelRegistry(mr *v1.ModelRegistry) (*v1.ModelRegistry, error) {
	mr.APIVersion = "v1"
	mr.Kind = "ModelRegistry"

	resp, err := c.doRequest(http.MethodPost, "/model_registries", mr)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to create model registry: status %d, body: %s", resp.StatusCode, string(body))
	}

	var results []v1.ModelRegistry
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if len(results) == 0 {
		return nil, fmt.Errorf("empty response when creating model registry")
	}

	return &results[0], nil
}

// GetModelRegistry retrieves a model registry by workspace and name.
func (c *Client) GetModelRegistry(workspace, name string) (*v1.ModelRegistry, error) {
	path := fmt.Sprintf("/model_registries?metadata->>workspace=eq.%s&metadata->>name=eq.%s",
		url.QueryEscape(workspace), url.QueryEscape(name))

	resp, err := c.doRequest(http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to get model registry: status %d, body: %s", resp.StatusCode, string(body))
	}

	var results []v1.ModelRegistry
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if len(results) == 0 {
		return nil, fmt.Errorf("model registry not found: %s/%s", workspace, name)
	}

	return &results[0], nil
}

// DeleteModelRegistry soft-deletes a model registry.
func (c *Client) DeleteModelRegistry(workspace, name string) error {
	mr, err := c.GetModelRegistry(workspace, name)
	if err != nil {
		return err
	}

	mr.Metadata.DeletionTimestamp = time.Now().Format(time.RFC3339)

	path := fmt.Sprintf("/model_registries?id=eq.%d", mr.ID)
	resp, err := c.doRequest(http.MethodPatch, path, mr)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to delete model registry: status %d, body: %s", resp.StatusCode, string(body))
	}

	return nil
}

// CreateEndpoint creates a new endpoint.
func (c *Client) CreateEndpoint(ep *v1.Endpoint) (*v1.Endpoint, error) {
	ep.APIVersion = "v1"
	ep.Kind = "Endpoint"

	resp, err := c.doRequest(http.MethodPost, "/endpoints", ep)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to create endpoint: status %d, body: %s", resp.StatusCode, string(body))
	}

	var results []v1.Endpoint
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if len(results) == 0 {
		return nil, fmt.Errorf("empty response when creating endpoint")
	}

	return &results[0], nil
}

// GetEndpoint retrieves an endpoint by workspace and name.
func (c *Client) GetEndpoint(workspace, name string) (*v1.Endpoint, error) {
	path := fmt.Sprintf("/endpoints?metadata->>workspace=eq.%s&metadata->>name=eq.%s",
		url.QueryEscape(workspace), url.QueryEscape(name))

	resp, err := c.doRequest(http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to get endpoint: status %d, body: %s", resp.StatusCode, string(body))
	}

	var results []v1.Endpoint
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if len(results) == 0 {
		return nil, fmt.Errorf("endpoint not found: %s/%s", workspace, name)
	}

	return &results[0], nil
}

// DeleteEndpoint soft-deletes an endpoint.
func (c *Client) DeleteEndpoint(workspace, name string) error {
	ep, err := c.GetEndpoint(workspace, name)
	if err != nil {
		return err
	}

	ep.Metadata.DeletionTimestamp = time.Now().Format(time.RFC3339)

	path := fmt.Sprintf("/endpoints?id=eq.%d", ep.ID)
	resp, err := c.doRequest(http.MethodPatch, path, ep)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to delete endpoint: status %d, body: %s", resp.StatusCode, string(body))
	}

	return nil
}

// CreateAPIKey creates a new API key via PostgREST RPC.
func (c *Client) CreateAPIKey(workspace, name string, quota int64) (string, error) {
	// Call the database RPC function
	// The RPC endpoint is at /rest/v1/rpc/create_api_key relative to the base URL
	// Extract base URL by removing /api/v1 suffix if present
	baseURL := c.apiEndpoint
	if idx := strings.Index(baseURL, "/api/v1"); idx != -1 {
		baseURL = baseURL[:idx]
	}
	rpcURL := strings.TrimSuffix(baseURL, "/") + "/rest/v1/rpc/create_api_key"

	payload := map[string]any{
		"p_workspace": workspace,
		"p_name":      name,
		"p_quota":     quota,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal API key payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, rpcURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("failed to create API key request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send API key request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("failed to create API key: status %d, body: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Status struct {
			SkValue string `json:"sk_value"`
		} `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to decode API key response: %w", err)
	}

	if result.Status.SkValue == "" {
		return "", fmt.Errorf("API key sk_value is empty in response")
	}

	return result.Status.SkValue, nil
}

// ChatCompletionRequest represents a chat completion request.
type ChatCompletionRequest struct {
	Model     string        `json:"model"`
	Messages  []ChatMessage `json:"messages"`
	MaxTokens int           `json:"max_tokens,omitempty"`
}

// ChatMessage represents a message in a chat completion request.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatCompletion sends a chat completion request to the endpoint.
func (c *Client) ChatCompletion(serviceURL, apiKey, model, message string) (string, error) {
	completionURL := strings.TrimSuffix(serviceURL, "/") + "/v1/chat/completions"

	payload := ChatCompletionRequest{
		Model: model,
		Messages: []ChatMessage{
			{Role: "user", Content: message},
		},
		MaxTokens: 50,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal chat request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, completionURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("failed to create chat request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	// Use longer timeout for chat completion
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send chat request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("chat completion failed: status %d, body: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to decode chat response: %w", err)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no choices in chat response")
	}

	return result.Choices[0].Message.Content, nil
}
