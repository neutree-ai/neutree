package model_registry

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

const (
	listModelUrl = "https://huggingface.co/api/models"
)

type huggingFace struct {
	apiToken string
	client   *http.Client
}

func newHuggingFace(registry *v1.ModelRegistry) *huggingFace {
	return &huggingFace{
		apiToken: registry.Spec.Credentials,
		client: &http.Client{
			Timeout: 300 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					//nolint:gosec
					InsecureSkipVerify: true,
				},
			},
		},
	}
}

func (hf *huggingFace) Connect() error {
	return nil
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
func (hf *huggingFace) ListModels(option ListOption) ([]v1.GeneralModel, error) {
	var (
		allHFModels []HuggingFaceModel
		result      []v1.GeneralModel
	)

	allHFModels, err := hf.getModelsList(option)
	if err != nil {
		return nil, err
	}

	for i := range allHFModels {
		result = append(result, v1.GeneralModel{
			Name: allHFModels[i].ModelID,
			Versions: []v1.Version{
				{
					Name:         "latest",
					CreationTime: allHFModels[i].CreatedAt.Format(time.RFC3339Nano),
				},
			},
		})
	}

	return result, nil
}

// HealthyCheck checks the health of the Hugging Face Hub API.
func (hf *huggingFace) HealthyCheck() bool {
	if _, err := hf.getModelsList(ListOption{Search: "", Limit: 1}); err != nil {
		return false
	}

	return true
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

	requestURL := listModelUrl + "?" + params.Encode()

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
