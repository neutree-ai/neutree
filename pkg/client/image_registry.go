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

// ImageRegistriesService handles communication with the image registry related endpoints
type ImageRegistriesService struct {
	client *Client
}

// NewImageRegistriesService creates a new image registries service
func NewImageRegistriesService(client *Client) *ImageRegistriesService {
	return &ImageRegistriesService{
		client: client,
	}
}

// ImageRegistryListOptions defines options for listing image registries
type ImageRegistryListOptions struct {
	Workspace string
	Name      string
}

// List lists all image registries in the specified workspace
func (s *ImageRegistriesService) List(opts ImageRegistryListOptions) ([]v1.ImageRegistry, error) {
	// Build URL with query parameters
	baseURL := fmt.Sprintf("%s/api/v1/image_registries", s.client.baseURL)

	params := url.Values{}
	if opts.Name != "" {
		params.Add("metadata->>name", "eq."+opts.Name)
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

	var imageRegistries []v1.ImageRegistry
	if err := json.NewDecoder(resp.Body).Decode(&imageRegistries); err != nil {
		return nil, err
	}

	return imageRegistries, nil
}

// Get retrieves detailed information about a specific image registry
func (s *ImageRegistriesService) Get(workspace, registryName string) (*v1.ImageRegistry, error) {
	url := fmt.Sprintf("%s/api/v1/image_registries?metadata->>name=eq.%s",
		s.client.baseURL, registryName)

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

	var imageRegistries []v1.ImageRegistry
	if err := json.NewDecoder(resp.Body).Decode(&imageRegistries); err != nil {
		return nil, err
	}

	if len(imageRegistries) == 0 {
		return nil, fmt.Errorf("image registry not found: %s", registryName)
	}

	return &imageRegistries[0], nil
}

// Create creates a new image registry
func (s *ImageRegistriesService) Create(workspace string, imageRegistry *v1.ImageRegistry) error {
	url := fmt.Sprintf("%s/api/v1/image_registries", s.client.baseURL)

	// Ensure workspace is set in metadata
	if imageRegistry.Metadata == nil {
		imageRegistry.Metadata = &v1.Metadata{}
	}

	imageRegistry.Metadata.Workspace = workspace

	jsonData, err := json.Marshal(imageRegistry)
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

// Update updates an existing image registry
func (s *ImageRegistriesService) Update(workspace, registryID string, imageRegistry *v1.ImageRegistry) error {
	url := fmt.Sprintf("%s/api/v1/image_registries?id=eq.%s",
		s.client.baseURL, registryID)

	jsonData, err := json.Marshal(imageRegistry)
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

// Delete removes a specific image registry
func (s *ImageRegistriesService) Delete(workspace, registryID string) error {
	url := fmt.Sprintf("%s/api/v1/image_registries?id=eq.%s",
		s.client.baseURL, registryID)

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
