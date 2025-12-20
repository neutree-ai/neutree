package client

import (
	v1 "github.com/neutree-ai/neutree/api/v1"
)

// ModelRegistriesService handles communication with the model registry related endpoints
type ModelRegistriesService struct {
	*resourceService
}

// NewModelRegistriesService creates a new model registries service
func NewModelRegistriesService(client *Client) *ModelRegistriesService {
	return &ModelRegistriesService{
		resourceService: newResourceService(client, "model_registries", "model registry"),
	}
}

// ModelRegistryListOptions defines options for listing model registries
type ModelRegistryListOptions struct {
	Workspace string
	Name      string
}

// List lists all model registries in the specified workspace
func (s *ModelRegistriesService) List(opts ModelRegistryListOptions) ([]v1.ModelRegistry, error) {
	var modelRegistries []v1.ModelRegistry

	params := buildListParams(ListOptions(opts))
	if err := s.list(params, &modelRegistries); err != nil {
		return nil, err
	}

	return modelRegistries, nil
}

// Get retrieves detailed information about a specific model registry
func (s *ModelRegistriesService) Get(workspace, modelRegistryName string) (*v1.ModelRegistry, error) {
	var modelRegistry v1.ModelRegistry
	if err := s.get(workspace, modelRegistryName, &modelRegistry); err != nil {
		return nil, err
	}

	return &modelRegistry, nil
}

// Create creates a new model registry
func (s *ModelRegistriesService) Create(workspace string, modelRegistry *v1.ModelRegistry) error {
	// Ensure workspace is set in metadata
	if modelRegistry.Metadata == nil {
		modelRegistry.Metadata = &v1.Metadata{}
	}

	modelRegistry.Metadata.Workspace = workspace

	return s.create(modelRegistry)
}

// Update updates an existing model registry
func (s *ModelRegistriesService) Update(modelRegistryID string, modelRegistry *v1.ModelRegistry) error {
	return s.update(modelRegistryID, modelRegistry)
}

// Delete removes a specific model registry
func (s *ModelRegistriesService) Delete(modelRegistryID string) error {
	return s.delete(modelRegistryID)
}
