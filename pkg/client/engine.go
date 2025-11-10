package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// EnginesService handles communication with the engine related endpoints
type EnginesService struct {
	client *Client
}

// NewEnginesService creates a new engines service
func NewEnginesService(client *Client) *EnginesService {
	return &EnginesService{
		client: client,
	}
}

// ListOptions defines options for listing engines
type ListOptions struct {
	Workspace string
	Name      string
}

// List lists all engines in the specified workspace
func (s *EnginesService) List(opts ListOptions) ([]v1.Engine, error) {
	// Build URL with query parameters
	baseURL := fmt.Sprintf("%s/api/v1/engines", s.client.baseURL)

	params := url.Values{}
	if opts.Name != "" {
		params.Add("metadata->>name", "eq."+opts.Name)
	}

	if opts.Workspace != "" {
		params.Add("metadata->>workspace", "eq."+opts.Workspace)
	}

	fullURL := baseURL
	if len(params) > 0 {
		fullURL = baseURL + "?" + params.Encode()
	}

	req, err := http.NewRequest("GET", fullURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := s.client.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("server returned non-200 status: %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	var engines []v1.Engine
	if err := json.NewDecoder(resp.Body).Decode(&engines); err != nil {
		return nil, err
	}

	return engines, nil
}

// Get retrieves detailed information about a specific engine
func (s *EnginesService) Get(workspace, engineName string) (*v1.Engine, error) {
	url := fmt.Sprintf("%s/api/v1/engines?metadata->>name=eq.%s&metadata->>workspace=eq.%s",
		s.client.baseURL, engineName, workspace)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := s.client.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("server returned non-200 status: %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	var engines []v1.Engine
	if err := json.NewDecoder(resp.Body).Decode(&engines); err != nil {
		return nil, err
	}

	if len(engines) == 0 {
		return nil, fmt.Errorf("engine not found: %s", engineName)
	}

	return &engines[0], nil
}

// Create creates a new engine
func (s *EnginesService) Create(workspace string, engine *v1.Engine) error {
	url := fmt.Sprintf("%s/api/v1/engines", s.client.baseURL)

	// Ensure workspace is set in metadata
	if engine.Metadata == nil {
		engine.Metadata = &v1.Metadata{}
	}

	engine.Metadata.Workspace = workspace

	jsonData, err := json.Marshal(engine)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned non-200/201 status: %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	return nil
}

// Update updates an existing engine
func (s *EnginesService) Update(workspace, engineID string, engine *v1.Engine) error {
	url := fmt.Sprintf("%s/api/v1/engines?id=eq.%s",
		s.client.baseURL, engineID)

	jsonData, err := json.Marshal(engine)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("PATCH", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned non-200/204 status: %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	return nil
}

// Delete removes a specific engine
func (s *EnginesService) Delete(workspace, engineID string) error {
	url := fmt.Sprintf("%s/api/v1/engines?id=eq.%s",
		s.client.baseURL, engineID)

	req, err := http.NewRequest("DELETE", url, nil)
	if err != nil {
		return err
	}

	resp, err := s.client.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned non-200/204 status: %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	return nil
}
