package storage

import (
	"encoding/json"

	"github.com/supabase-community/postgrest-go"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

const (
	ENDPOINT_TABLE       = "endpoints"
	ENGINE_TABLE         = "engines"
	IMAGE_REGISTRY_TABLE = "image_registries"
	OCLUSTERS_TABLE      = "clusters"
	MODEL_REGISTRY_TABLE = "model_registries"
)

type Storage struct {
	postgrestClient *postgrest.Client
}

type Options struct {
	AccessURL string
	Scheme    string
}

func New(o Options) *Storage {
	return &Storage{
		postgrestClient: postgrest.NewClient(o.AccessURL, o.Scheme, nil),
	}
}

type Filter struct {
	Column   string
	Operator string
	Value    string
}
type ListOption struct {
	Filters []Filter
}

func applyListOption(builder *postgrest.FilterBuilder, option ListOption) {
	for _, filter := range option.Filters {
		builder.Filter(filter.Column, filter.Operator, filter.Value)
	}
}

func (s *Storage) ListImageRegistry(option ListOption) ([]v1.ImageRegistry, error) {
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

func (s *Storage) CreateImageRegistry(data *v1.ImageRegistry) error {
	var (
		err error
	)

	if _, _, err = s.postgrestClient.From(IMAGE_REGISTRY_TABLE).Insert(data, true, "", "", "").Execute(); err != nil {
		return err
	}

	return nil
}

func (s *Storage) DeleteImageRegistry(id string) error {
	var (
		err error
	)

	if _, _, err = s.postgrestClient.From(IMAGE_REGISTRY_TABLE).Delete("", "").Filter("id", "eq", id).Execute(); err != nil {
		return err
	}

	return nil
}

func (s *Storage) UpdateImageRegistry(id string, data *v1.ImageRegistry) error {
	var (
		err error
	)

	if _, _, err = s.postgrestClient.From(IMAGE_REGISTRY_TABLE).Update(data, "", "").Filter("id", "eq", id).Execute(); err != nil {
		return err
	}

	return nil
}

func parseResponse(response interface{}, responseContent []byte) error {
	return json.Unmarshal(responseContent, response)
}
