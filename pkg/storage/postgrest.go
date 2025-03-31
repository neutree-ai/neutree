package storage

import (
	"encoding/json"

	"github.com/supabase-community/postgrest-go"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type postgrestStorage struct {
	postgrestClient *postgrest.Client
}

func (s *postgrestStorage) ListImageRegistry(option ListOption) ([]v1.ImageRegistry, error) {
	var (
		response []v1.ImageRegistry
		err      error
	)
	builder := s.postgrestClient.From(IMAGE_REGISTRY_TABLE).Select("*", "", false)
	applyListOption(builder, option)

	responseContent, _, err := builder.Execute()
	if err != nil {
		return nil, err
	}

	if err = parseResponse(&response, responseContent); err != nil {
		return nil, err
	}

	return response, nil
}

func (s *postgrestStorage) CreateImageRegistry(data *v1.ImageRegistry) error {
	var (
		err error
	)

	if _, _, err = s.postgrestClient.From(IMAGE_REGISTRY_TABLE).Insert(data, true, "", "", "").Execute(); err != nil {
		return err
	}

	return nil
}

func (s *postgrestStorage) DeleteImageRegistry(id string) error {
	var (
		err error
	)

	if _, _, err = s.postgrestClient.From(IMAGE_REGISTRY_TABLE).Delete("", "").Filter("id", "eq", id).Execute(); err != nil {
		return err
	}

	return nil
}

func (s *postgrestStorage) UpdateImageRegistry(id string, data *v1.ImageRegistry) error {
	var (
		err error
	)

	if _, _, err = s.postgrestClient.From(IMAGE_REGISTRY_TABLE).Update(data, "", "").Filter("id", "eq", id).Execute(); err != nil {
		return err
	}

	return nil
}

// ListModelRegistry lists all model registries with optional filters
func (s *postgrestStorage) ListModelRegistry(option ListOption) ([]v1.ModelRegistry, error) {
	var (
		response []v1.ModelRegistry
		err      error
	)
	builder := s.postgrestClient.From(MODEL_REGISTRY_TABLE).Select("*", "", false)
	applyListOption(builder, option)

	responseContent, _, err := builder.Execute()
	if err != nil {
		return nil, err
	}

	if err = parseResponse(&response, responseContent); err != nil {
		return nil, err
	}

	return response, nil
}

// CreateModelRegistry creates a new model registry
func (s *postgrestStorage) CreateModelRegistry(data *v1.ModelRegistry) error {
	var (
		err error
	)

	if _, _, err = s.postgrestClient.From(MODEL_REGISTRY_TABLE).Insert(data, true, "", "", "").Execute(); err != nil {
		return err
	}

	return nil
}

// DeleteModelRegistry deletes a model registry by ID
func (s *postgrestStorage) DeleteModelRegistry(id string) error {
	var (
		err error
	)

	if _, _, err = s.postgrestClient.From(MODEL_REGISTRY_TABLE).Delete("", "").Filter("id", "eq", id).Execute(); err != nil {
		return err
	}

	return nil
}

// UpdateModelRegistry updates a model registry by ID
func (s *postgrestStorage) UpdateModelRegistry(id string, data *v1.ModelRegistry) error {
	var (
		err error
	)

	if _, _, err = s.postgrestClient.From(MODEL_REGISTRY_TABLE).Update(data, "", "").Filter("id", "eq", id).Execute(); err != nil {
		return err
	}

	return nil
}

func parseResponse(response interface{}, responseContent []byte) error {
	return json.Unmarshal(responseContent, response)
}
