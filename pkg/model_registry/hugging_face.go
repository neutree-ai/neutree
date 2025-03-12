package model_registry

import (
	"encoding/json"
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
}

func newHuggingFace(registry *v1.ModelRegistry) *huggingFace {
	return &huggingFace{
		apiToken: registry.Spec.Credentials,
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
	Likes         int       `json:"likes"`
	TrendingScore int       `json:"trendingScore"`
	Private       bool      `json:"private"`
	Downloads     int       `json:"downloads"`
	Tags          []string  `json:"tags"`
	PipelineTag   string    `json:"pipeline_tag"`
	LibraryName   string    `json:"library_name"`
	CreatedAt     time.Time `json:"createdAt"`
	ModelID       string    `json:"modelId"`
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

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var models []HuggingFaceModel

	if err = json.Unmarshal(body, &models); err != nil {
		return nil, err
	}

	return models, nil
}
