package storage

import (
	"github.com/supabase-community/postgrest-go"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

const (
	ENDPOINT_TABLE       = "endpoints"
	ENGINE_TABLE         = "engines"
	IMAGE_REGISTRY_TABLE = "image_registries"
	CLUSTERS_TABLE       = "clusters"
	MODEL_REGISTRY_TABLE = "model_registries"
)

type ImageRegistryStorage interface {
	// CreateImageRegistry creates a new image registry in the database.
	CreateImageRegistry(data *v1.ImageRegistry) error
	// DeleteImageRegistry deletes an image registry by its ID.
	DeleteImageRegistry(id string) error
	// UpdateImageRegistry updates an existing image registry in the database.
	UpdateImageRegistry(id string, data *v1.ImageRegistry) error
	// ListImageRegistry retrieves a list of image registries with optional filters.
	ListImageRegistry(option ListOption) ([]v1.ImageRegistry, error)
}

type ModelRegistryStorage interface {
	// CreateModelRegistry creates a new model registry in the database.
	CreateModelRegistry(data *v1.ModelRegistry) error
	// DeleteModelRegistry deletes a model registry by its ID.
	DeleteModelRegistry(id string) error
	// UpdateModelRegistry updates an existing model registry in the database.
	UpdateModelRegistry(id string, data *v1.ModelRegistry) error
	// ListModelRegistry retrieves a list of model registries with optional filters.
	ListModelRegistry(option ListOption) ([]v1.ModelRegistry, error)
}

type Storage interface {
	ImageRegistryStorage
	ModelRegistryStorage
}

type Options struct {
	AccessURL string
	Scheme    string
}

func New(o Options) (Storage, error) {
	postgrestClient := postgrest.NewClient(o.AccessURL, o.Scheme, nil)

	return &postgrestStorage{
		postgrestClient: postgrestClient,
	}, nil
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
