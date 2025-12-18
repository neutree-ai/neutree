package client

import (
	v1 "github.com/neutree-ai/neutree/api/v1"
)

// EnginesService handles communication with the engine related endpoints
type EnginesService struct {
	*resourceService
}

// NewEnginesService creates a new engines service
func NewEnginesService(client *Client) *EnginesService {
	return &EnginesService{
		resourceService: newResourceService(client, "engines", "engine"),
	}
}

// List lists all engines in the specified workspace
func (s *EnginesService) List(opts ListOptions) ([]v1.Engine, error) {
	var engines []v1.Engine

	params := buildListParams(opts)
	if err := s.list(params, &engines); err != nil {
		return nil, err
	}

	return engines, nil
}

// Get retrieves detailed information about a specific engine
func (s *EnginesService) Get(workspace, engineName string) (*v1.Engine, error) {
	var engine v1.Engine
	if err := s.get(workspace, engineName, &engine); err != nil {
		return nil, err
	}

	return &engine, nil
}

// Create creates a new engine
func (s *EnginesService) Create(workspace string, engine *v1.Engine) error {
	// Ensure workspace is set in metadata
	if engine.Metadata == nil {
		engine.Metadata = &v1.Metadata{}
	}

	engine.Metadata.Workspace = workspace

	return s.create(engine)
}

// Update updates an existing engine
func (s *EnginesService) Update(engineID string, engine *v1.Engine) error {
	return s.update(engineID, engine)
}

// Delete removes a specific engine
func (s *EnginesService) Delete(engineID string) error {
	return s.delete(engineID)
}
