package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// resourceService provides common CRUD operations for API resources
type resourceService struct {
	client       *Client
	baseUrl      string
	resourceName string // for error messages, e.g., "engine", "model registry"
}

// newResourceService creates a new resource service
func newResourceService(client *Client, endpoint, resourceName string) *resourceService {
	return &resourceService{
		client:       client,
		baseUrl:      fmt.Sprintf("%s/api/v1/%s", client.baseURL, endpoint),
		resourceName: resourceName,
	}
}

// ListOptions defines common options for listing resources
type ListOptions struct {
	Workspace string
	Name      string
}

// list retrieves a list of resources based on query parameters
func (s *resourceService) list(params url.Values, result interface{}) error {
	url := s.baseUrl
	if len(params) > 0 {
		url = s.baseUrl + "?" + params.Encode()
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}

	resp, err := s.client.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned non-200 status: %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
		return err
	}

	return nil
}

// get retrieves a single resource by name and workspace
func (s *resourceService) get(workspace, name string, result interface{}) error {
	if name == "" {
		return fmt.Errorf("%s name cannot be empty", s.resourceName)
	}

	if workspace == "" {
		return fmt.Errorf("workspace cannot be empty")
	}

	params := url.Values{}
	params.Add("metadata->>name", "eq."+name)
	params.Add("metadata->>workspace", "eq."+workspace)

	url := s.baseUrl + "?" + params.Encode()

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}

	resp, err := s.client.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned non-200 status: %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	// Decode into a slice first to check if resource exists
	var items []json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return err
	}

	if len(items) == 0 {
		return fmt.Errorf("%s not found: %s", s.resourceName, name)
	}

	// Unmarshal the first item into the result
	if err := json.Unmarshal(items[0], result); err != nil {
		return err
	}

	return nil
}

// create creates a new resource
func (s *resourceService) create(data interface{}) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", s.baseUrl, bytes.NewBuffer(jsonData))
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

// update updates an existing resource by ID
func (s *resourceService) update(id string, data interface{}) error {
	if id == "" {
		return fmt.Errorf("%s ID cannot be empty", s.resourceName)
	}

	params := url.Values{}
	params.Add("id", "eq."+id)

	url := s.baseUrl + "?" + params.Encode()

	jsonData, err := json.Marshal(data)
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

// delete removes a resource by ID
func (s *resourceService) delete(id string) error {
	if id == "" {
		return fmt.Errorf("%s ID cannot be empty", s.resourceName)
	}

	params := url.Values{}
	params.Add("id", "eq."+id)

	url := s.baseUrl + "?" + params.Encode()

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

// buildListParams builds query parameters for list operations
func buildListParams(opts ListOptions) url.Values {
	params := url.Values{}
	if opts.Name != "" {
		params.Add("metadata->>name", "eq."+opts.Name)
	}

	if opts.Workspace != "" {
		params.Add("metadata->>workspace", "eq."+opts.Workspace)
	}

	return params
}
