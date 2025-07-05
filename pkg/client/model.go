package client

import (
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// ModelsService handles communication with the model related endpoints
type ModelsService struct {
	client *Client
}

// NewModelsService creates a new models service
func NewModelsService(client *Client) *ModelsService {
	return &ModelsService{
		client: client,
	}
}

// List lists all models in the specified registry
func (s *ModelsService) List(workspace, registry, search string) ([]v1.GeneralModel, error) {
	url := fmt.Sprintf("%s/api/v1/workspaces/%s/model_registries/%s/models", s.client.baseURL, workspace, registry)
	if search != "" {
		url = fmt.Sprintf("%s?search=%s", url, search)
	}

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

	var models []v1.GeneralModel
	if err := json.NewDecoder(resp.Body).Decode(&models); err != nil {
		return nil, err
	}

	return models, nil
}

// Get retrieves detailed information about a specific model version
func (s *ModelsService) Get(workspace, registry, modelName, version string) (*v1.ModelVersion, error) {
	url := fmt.Sprintf("%s/api/v1/workspaces/%s/model_registries/%s/models/%s", s.client.baseURL, workspace, registry, modelName)
	if version != "" && version != v1.LatestVersion {
		url = fmt.Sprintf("%s?version=%s", url, version)
	}

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

	var modelVersion v1.ModelVersion
	if err := json.NewDecoder(resp.Body).Decode(&modelVersion); err != nil {
		return nil, err
	}

	return &modelVersion, nil
}

// Delete removes a specific model from the registry
func (s *ModelsService) Delete(workspace, registry, modelName, version string) error {
	url := fmt.Sprintf("%s/api/v1/workspaces/%s/model_registries/%s/models/%s", s.client.baseURL, workspace, registry, modelName)
	if version != "" && version != v1.LatestVersion {
		url = fmt.Sprintf("%s?version=%s", url, version)
	}

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

// PushWithProgress uploads a model to the registry with progress reporting
// Returns a reader for import progress updates after upload completes
func (s *ModelsService) PushWithProgress(
	workspace, registry, modelPath, name, version, description string, labels map[string]string, progressWriter io.Writer,
) (io.Reader, error) {
	file, err := os.Open(modelPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// Create IO pipe for multipart writer
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)

	// IO copy goroutine
	go func() {
		defer pw.Close()

		_ = mw.WriteField("name", name)
		_ = mw.WriteField("version", version)

		if description != "" {
			_ = mw.WriteField("description", description)
		}

		if len(labels) > 0 {
			labelsJSON, _ := json.Marshal(labels)
			_ = mw.WriteField("labels", string(labelsJSON))
		}

		part, _ := mw.CreateFormFile("model", filepath.Base(modelPath))

		// Use TeeReader to copy data and update progress simultaneously
		var reader io.Reader = file
		if progressWriter != nil {
			reader = io.TeeReader(file, progressWriter)
		}

		buf := make([]byte, 16*1024*1024)

		_, err := io.CopyBuffer(part, reader, buf)
		if err != nil {
			pw.CloseWithError(err)
			return
		}

		mw.Close()
	}()

	url := fmt.Sprintf("%s/api/v1/workspaces/%s/model_registries/%s/models",
		s.client.baseURL, workspace, registry)

	req, err := http.NewRequest("POST", url, pr)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", mw.FormDataContentType())

	// Send request
	resp, err := s.client.do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		return nil, fmt.Errorf("server returned non-200 status: %d, body: %s",
			resp.StatusCode, string(bodyBytes))
	}

	// Return the response body for the caller to read import progress
	return resp.Body, nil
}

// Pull downloads a model from the registry
func (s *ModelsService) Pull(workspace, registry, modelName, version, outputDir string) error {
	// Create request
	url := fmt.Sprintf("%s/api/v1/workspaces/%s/model_registries/%s/models/%s/download", s.client.baseURL, workspace, registry, modelName)
	if version != "" && version != v1.LatestVersion {
		url = fmt.Sprintf("%s?version=%s", url, version)
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

	// Parse filename
	contentDisposition := resp.Header.Get("Content-Disposition")
	filename := ""

	if contentDisposition != "" {
		if strings.Contains(contentDisposition, "filename=") {
			filename = strings.Split(strings.Split(contentDisposition, "filename=")[1], ";")[0]
			filename = strings.Trim(filename, "\"")
		}
	}

	if filename == "" {
		filename = fmt.Sprintf("%s-%s.bentomodel", modelName, version)
	}

	// Create output file
	outputPath := filepath.Join(outputDir, filename)

	out, err := os.Create(outputPath)
	if err != nil {
		return err
	}

	defer out.Close()

	// Save file
	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return err
	}

	fmt.Printf("Model saved to %s\n", outputPath)

	return nil
}

// ParseModelTag parses a model tag string into model name and version
func ParseModelTag(modelTag string) (name string, version string, err error) {
	parts := strings.Split(modelTag, ":")
	if len(parts) == 1 {
		return parts[0], v1.LatestVersion, nil
	} else if len(parts) == 2 {
		return parts[0], parts[1], nil
	}

	return "", "", fmt.Errorf("invalid model tag format, expected name:version")
}
