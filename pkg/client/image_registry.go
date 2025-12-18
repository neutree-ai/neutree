package client

import (
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// ImageRegistriesService handles communication with the image registry related endpoints
type ImageRegistriesService struct {
	*resourceService
}

// NewImageRegistriesService creates a new image registries service
func NewImageRegistriesService(client *Client) *ImageRegistriesService {
	return &ImageRegistriesService{
		resourceService: newResourceService(client, "image_registries", "image registry"),
	}
}

// ImageRegistryListOptions defines options for listing image registries
type ImageRegistryListOptions struct {
	Workspace string
	Name      string
	WithCreds bool
}

// List lists all image registries in the specified workspace
func (s *ImageRegistriesService) List(opts ImageRegistryListOptions) ([]v1.ImageRegistry, error) {
	var imageRegistries []v1.ImageRegistry

	// Use credentials endpoint if WithCreds is true
	if opts.WithCreds {
		credService := &resourceService{
			client:       s.client,
			baseUrl:      fmt.Sprintf("%s/api/v1/credentials/image_registries", s.client.baseURL),
			resourceName: s.resourceName,
		}
		params := buildListParams(ListOptions{Workspace: opts.Workspace, Name: opts.Name})

		if err := credService.list(params, &imageRegistries); err != nil {
			return nil, err
		}

		return imageRegistries, nil
	}

	params := buildListParams(ListOptions{Workspace: opts.Workspace, Name: opts.Name})
	if err := s.list(params, &imageRegistries); err != nil {
		return nil, err
	}

	return imageRegistries, nil
}

// Get retrieves detailed information about a specific image registry
func (s *ImageRegistriesService) Get(workspace, registryName string) (*v1.ImageRegistry, error) {
	var imageRegistry v1.ImageRegistry
	if err := s.get(workspace, registryName, &imageRegistry); err != nil {
		return nil, err
	}

	return &imageRegistry, nil
}

// Create creates a new image registry
func (s *ImageRegistriesService) Create(workspace string, imageRegistry *v1.ImageRegistry) error {
	// Ensure workspace is set in metadata
	if imageRegistry.Metadata == nil {
		imageRegistry.Metadata = &v1.Metadata{}
	}

	imageRegistry.Metadata.Workspace = workspace

	return s.create(imageRegistry)
}

// Update updates an existing image registry
func (s *ImageRegistriesService) Update(registryID string, imageRegistry *v1.ImageRegistry) error {
	return s.update(registryID, imageRegistry)
}

// Delete removes a specific image registry
func (s *ImageRegistriesService) Delete(registryID string) error {
	return s.delete(registryID)
}
