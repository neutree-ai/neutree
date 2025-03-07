package postgrest

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

type API struct {
	*postgrest.Client
}

type Options struct {
	AccessURL string
	Scheme    string
}

func NewAPI(o Options) *API {
	return &API{
		Client: postgrest.NewClient(o.AccessURL, o.Scheme, nil),
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

func (api *API) ListImageRegistry(option ListOption) ([]v1.ImageRegistry, error) {
	var (
		response []v1.ImageRegistry
		err      error
	)
	builder := api.From(IMAGE_REGISTRY_TABLE).Select("*", "", false)
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

func (api *API) CreateImageRegistry(data *v1.ImageRegistry) error {
	var (
		err error
	)

	if _, _, err = api.From(IMAGE_REGISTRY_TABLE).Insert(data, true, "", "", "").Execute(); err != nil {
		return err
	}

	return nil
}

func (api *API) DeleteImageRegistry(id string) error {
	var (
		err error
	)

	if _, _, err = api.From(IMAGE_REGISTRY_TABLE).Delete("", "").Filter("id", "eq", id).Execute(); err != nil {
		return err
	}

	return nil
}

func (api *API) UpdateImageRegistry(id string, data *v1.ImageRegistry) error {
	var (
		err error
	)

	if _, _, err = api.From(IMAGE_REGISTRY_TABLE).Update(data, "", "").Filter("id", "eq", id).Execute(); err != nil {
		return err
	}

	return nil
}

func parseResponse(response interface{}, responseContent []byte) error {
	return json.Unmarshal(responseContent, response)
}
