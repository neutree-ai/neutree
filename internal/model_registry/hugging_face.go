package model_registry

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

var (
	sharedClient = func() *http.Client {
		transport := http.DefaultTransport.(*http.Transport).Clone() //nolint:errcheck
		transport.TLSClientConfig = &tls.Config{
			//nolint:gosec
			InsecureSkipVerify: true,
		}
		transport.IdleConnTimeout = 30 * time.Second

		return &http.Client{
			Timeout:   300 * time.Second,
			Transport: transport,
		}
	}()
)

const (
	listModelPath = "/api/models"
	whoamiPath    = "/api/whoami-v2"
)

// errHuggingFaceNotSupported wraps ErrNotSupported so a caller can tell "a
// public registry cannot do this" from "it tried and failed", and answer with a
// clear refusal instead of a server error. Hugging Face is read-only here; the
// write and detail operations are not implemented against it.
var errHuggingFaceNotSupported = errors.Wrap(ErrNotSupported, "operation not supported for Hugging Face registry")

type huggingFace struct {
	apiToken string
	client   *http.Client
	url      string
}

func newHuggingFace(registry *v1.ModelRegistry) (*huggingFace, error) {
	if registry.Spec.Url == "" {
		return nil, errors.New("registry.Spec.Url cannot be empty")
	}

	parsedUrl, err := url.Parse(registry.Spec.Url)
	if err != nil {
		return nil, errors.Wrap(err, "invalid registry.Spec.Url")
	}

	if parsedUrl.Host == "" || parsedUrl.Scheme == "" {
		return nil, errors.New("invalid registry.Spec.Url")
	}

	return &huggingFace{
		url:      strings.TrimSuffix(parsedUrl.String(), "/"),
		apiToken: registry.Spec.Credentials,
		client:   sharedClient,
	}, nil
}

func (hf *huggingFace) Connect() error {
	// Perform a health check to validate the connection
	return hf.healthyCheck()
}

func (hf *huggingFace) Disconnect() error {
	return nil
}

type HuggingFaceModel struct {
	ID            string    `json:"_id"`
	ID0           string    `json:"id"`
	Likes         int       `json:"likes,omitempty"`
	TrendingScore float64   `json:"trendingScore,omitempty"`
	Private       bool      `json:"private,omitempty"`
	Downloads     int       `json:"downloads,omitempty"`
	Tags          []string  `json:"tags,omitempty"`
	PipelineTag   string    `json:"pipeline_tag,omitempty"`
	LibraryName   string    `json:"library_name,omitempty"`
	CreatedAt     time.Time `json:"createdAt,omitempty"`
	ModelID       string    `json:"modelId,omitempty"`
}

// ListModels retrieves all models from the Hugging Face Hub API by page.
//
// The Hub does not report how many models match a search, so Total is the size
// of the page that came back. A client cannot use it to compute a page count for
// a public registry.
func (hf *huggingFace) ListModels(option ListOption) (*ModelPage, error) {
	var (
		allHFModels []HuggingFaceModel
		result      = []v1.GeneralModel{}
	)

	allHFModels, err := hf.getModelsList(option)
	if err != nil {
		return nil, err
	}

	for i := range allHFModels {
		result = append(result, v1.GeneralModel{
			Name: allHFModels[i].ModelID,
			Versions: []v1.ModelVersion{
				{
					Name:         v1.LatestVersion,
					CreationTime: allHFModels[i].CreatedAt.Format(time.RFC3339Nano),
				},
			},
		})
	}

	return &ModelPage{Models: result, Total: len(result)}, nil
}

// HealthyCheck checks the health of the Hugging Face Hub API.
func (hf *huggingFace) HealthyCheck() error {
	return hf.healthyCheck()
}

func (hf *huggingFace) healthyCheck() error {
	if hf.apiToken != "" {
		// Validate the token by making a simple request
		_, err := hf.whoami()
		if err != nil {
			return errors.Wrap(err, "invalid Hugging Face API token")
		}
	}

	_, err := hf.getModelsList(ListOption{Search: "", Limit: 1})
	if err != nil {
		return errors.Wrap(err, "failed to list models from Hugging Face API")
	}

	return nil
}

// GetModelsList calls the Hugging Face Hub API to get a list of models with pagination.
func (hf *huggingFace) getModelsList(options ListOption) ([]HuggingFaceModel, error) {
	params := url.Values{}
	if options.Limit != 0 {
		params.Add("limit", strconv.Itoa(options.Limit))
	}

	if options.Search != "" {
		params.Add("search", options.Search)
	}

	requestURL := hf.url + listModelPath + "?" + params.Encode()

	req, err := http.NewRequest("GET", requestURL, nil)
	if err != nil {
		return nil, err
	}

	if hf.apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+hf.apiToken)
	}

	resp, err := hf.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to list models: %s", string(body))
	}

	var models []HuggingFaceModel

	if err = json.Unmarshal(body, &models); err != nil {
		return nil, err
	}

	return models, nil
}

func (hf *huggingFace) whoami() (string, error) {
	req, err := http.NewRequest("GET", hf.url+whoamiPath, nil)
	if err != nil {
		return "", err
	}

	req.Header.Set("Authorization", "Bearer "+hf.apiToken)

	resp, err := hf.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to validate Hugging Face API token: %s", string(body))
	}

	var result struct {
		Name string `json:"name"`
	}

	if err = json.Unmarshal(body, &result); err != nil {
		return "", err
	}

	return result.Name, nil
}

// Implement the remaining ModelRegistry interface methods with "not supported" errors

func (hf *huggingFace) GetModelVersion(name, version string) (*v1.ModelVersion, error) {
	return nil, errHuggingFaceNotSupported
}

// GetModelDetail is not implemented for HuggingFace: reading a public
// checkpoint's files would mean downloading them.
func (hf *huggingFace) GetModelDetail(name, version string) (*v1.ModelVersion, error) {
	return nil, errHuggingFaceNotSupported
}

// GetReadme is not implemented for HuggingFace.
func (hf *huggingFace) GetReadme(name, version string) (string, error) {
	return "", errHuggingFaceNotSupported
}

// DeleteModel returns an error for HuggingFace as it's read-only
func (hf *huggingFace) DeleteModel(name, version string) error {
	return errHuggingFaceNotSupported
}

// ImportModel returns an error for HuggingFace as it's read-only
func (hf *huggingFace) ImportModel(reader io.Reader, name, version string, progress io.Writer) error {
	return errHuggingFaceNotSupported
}

// ExportModel returns an error for HuggingFace as it's read-only
func (hf *huggingFace) ExportModel(name, version, outputPath string) error {
	return errHuggingFaceNotSupported
}

// GetModelPath returns an error for HuggingFace as it's read-only
func (hf *huggingFace) GetModelPath(name, version string) (string, error) {
	return "", errHuggingFaceNotSupported
}

// SetManualModelInfo returns an error for HuggingFace as it's read-only
func (hf *huggingFace) SetManualModelInfo(name, version string, info *v1.ModelInfo) error {
	return errHuggingFaceNotSupported
}

// CollectUsage is not implemented for HuggingFace: the Hub's storage is not ours
// to measure, and the model count is unbounded.
func (hf *huggingFace) CollectUsage() (*RegistryUsage, error) {
	return nil, errHuggingFaceNotSupported
}

func (hf *huggingFace) GetNFSVersion() (string, error) {
	return "", nil
}
