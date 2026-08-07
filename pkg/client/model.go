package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// ModelsService handles communication with the model related endpoints
type ModelsService struct {
	client *Client
}

type finalizePushRequest struct {
	Version      string `json:"version"`
	CreationTime string `json:"creation_time,omitempty"`
	Size         string `json:"size,omitempty"`
	Module       string `json:"module,omitempty"`
}

func (s *ModelsService) FinalizePush(workspace, registry, modelName string, modelVersion *v1.ModelVersion) error {
	if modelVersion == nil {
		return fmt.Errorf("model version is required")
	}

	body, err := json.Marshal(finalizePushRequest{
		Version:      modelVersion.Name,
		CreationTime: modelVersion.CreationTime,
		Size:         modelVersion.Size,
		Module:       modelVersion.Module,
	})
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/api/v1/workspaces/%s/model_registries/%s/models/%s/finalize", s.client.baseURL, workspace, registry, modelName)

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(body))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned non-204 status: %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	return nil
}

// NewModelsService creates a new models service
func NewModelsService(client *Client) *ModelsService {
	return &ModelsService{
		client: client,
	}
}

// ModelListOptions narrows a model listing. A zero Limit asks for the server's
// default page size, and a zero Offset for the first page.
type ModelListOptions struct {
	Search string
	Limit  int
	Offset int
}

// ModelList is one page of a model listing.
//
// Raw is the response body exactly as the server sent it. Callers that render
// the payload rather than read fields off it should use Raw: a decode/re-encode
// round trip through the structs below silently rewrites the payload (dropping
// fields this build does not know about, and materialising ones the server
// omitted), which is precisely what a machine-readable output format must not do.
type ModelList struct {
	Models []v1.GeneralModel
	Raw    json.RawMessage

	// Offset is the index of the first item of this page within the full result
	// set, and Total the size of that set. Both come from the Content-Range
	// response header; the body stays a bare array. Total is nil when the
	// registry cannot count what matched (the header carries "*" in its place),
	// which is not the same as counting zero.
	//
	// When the header names no range — it is absent, malformed, or reports the
	// empty page as "*" — Offset falls back to the offset that was asked for.
	// Reporting position 0 for a page fetched from offset 20 would be a wrong
	// number rather than an absent one.
	Offset int
	Total  *int
}

// List lists models in the specified registry.
func (s *ModelsService) List(workspace, registry string, opts ModelListOptions) (*ModelList, error) {
	endpoint := fmt.Sprintf("%s/api/v1/workspaces/%s/model_registries/%s/models", s.client.baseURL, workspace, registry)

	query := url.Values{}
	if opts.Search != "" {
		query.Set("search", opts.Search)
	}

	if opts.Limit > 0 {
		query.Set("limit", strconv.Itoa(opts.Limit))
	}

	if opts.Offset > 0 {
		query.Set("offset", strconv.Itoa(opts.Offset))
	}

	if len(query) > 0 {
		endpoint = endpoint + "?" + query.Encode()
	}

	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return nil, err
	}

	resp, err := s.client.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, responseError(resp.StatusCode, body)
	}

	result := &ModelList{Raw: body, Offset: opts.Offset}
	if err := json.Unmarshal(body, &result.Models); err != nil {
		return nil, err
	}

	offset, offsetKnown, total := parseContentRange(resp.Header.Get("Content-Range"))
	if offsetKnown {
		result.Offset = offset
	}

	result.Total = total

	return result, nil
}

// parseContentRange reads the PostgREST-style "first-last/total" header the
// listing endpoint answers with, returning the offset of the page, whether the
// header named it at all, and the total number of matches.
//
// The two unknowns are reported separately because they arise separately: a
// public registry answers "0-9/*", naming the range but unable to count the
// catalogue behind it, while a server that predates the header names neither. A
// nil total therefore says nothing about whether the offset is trustworthy.
func parseContentRange(header string) (offset int, offsetKnown bool, total *int) {
	rangePart, totalPart, found := strings.Cut(strings.TrimSpace(header), "/")
	if !found {
		return 0, false, nil
	}

	if first, _, ok := strings.Cut(rangePart, "-"); ok {
		if parsed, err := strconv.Atoi(first); err == nil && parsed >= 0 {
			offset, offsetKnown = parsed, true
		}
	}

	if parsed, err := strconv.Atoi(totalPart); err == nil && parsed >= 0 {
		total = &parsed
	}

	return offset, offsetKnown, total
}

// responseError turns a failed model request into an error carrying what the
// server actually said. These endpoints answer with a JSON body holding a
// "message", and some of those messages are the whole point of the response —
// a registry refusing to page from an offset is stating a limit, not failing —
// so the message leads and the status code follows it in parentheses.
func responseError(statusCode int, body []byte) error {
	var payload struct {
		Message string `json:"message"`
	}

	if err := json.Unmarshal(body, &payload); err == nil && payload.Message != "" {
		return fmt.Errorf("%s (HTTP %d)", payload.Message, statusCode)
	}

	return fmt.Errorf("server returned non-200 status: %d, body: %s", statusCode, string(body))
}

// ModelDetail is one model version's detail response. As with ModelList, Raw is
// the untouched response body and is what machine-readable output must render.
type ModelDetail struct {
	Version *v1.ModelVersion
	Raw     json.RawMessage
}

// Get retrieves detailed information about a specific model version
func (s *ModelsService) Get(workspace, registry, modelName, version string) (*ModelDetail, error) {
	endpoint := fmt.Sprintf("%s/api/v1/workspaces/%s/model_registries/%s/models/%s",
		s.client.baseURL, workspace, registry, modelName)
	if version != "" && version != v1.LatestVersion {
		endpoint = endpoint + "?" + url.Values{"version": []string{version}}.Encode()
	}

	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return nil, err
	}

	resp, err := s.client.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, responseError(resp.StatusCode, body)
	}

	modelVersion := &v1.ModelVersion{}
	if err := json.Unmarshal(body, modelVersion); err != nil {
		return nil, err
	}

	return &ModelDetail{Version: modelVersion, Raw: body}, nil
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

		if info, err := file.Stat(); err == nil {
			_ = mw.WriteField("model_size", strconv.FormatInt(info.Size(), 10))
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
