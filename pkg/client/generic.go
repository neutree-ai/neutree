package client

import (
	"encoding/json"
	"fmt"
	"net/url"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/scheme"
)

// GenericService provides kind-based generic CRUD operations for apply workflows.
type GenericService struct {
	client *Client
	scheme *scheme.Scheme
}

// NewGenericService creates a new GenericService.
func NewGenericService(client *Client, s *scheme.Scheme) *GenericService {
	return &GenericService{
		client: client,
		scheme: s,
	}
}

// kindEndpoint maps a Kind to its REST API endpoint path segment.
var kindEndpoint = map[string]string{
	"Workspace":      "workspaces",
	"Engine":         "engines",
	"Cluster":        "clusters",
	"Endpoint":       "endpoints",
	"ImageRegistry":  "image_registries",
	"ModelRegistry":  "model_registries",
	"ModelCatalog":   "model_catalogs",
	"Role":           "roles",
	"RoleAssignment": "role_assignments",
	"OEMConfig":      "oem_configs",
}

// unsupportedKinds are kinds that cannot be created via the REST API.
var unsupportedKinds = map[string]string{
	"ApiKey":      "ApiKey only supports GET/PATCH, not POST creation",
	"UserProfile": "UserProfile is created through the auth system, not the REST API",
}

func endpointForKind(kind string) (string, error) {
	if reason, ok := unsupportedKinds[kind]; ok {
		return "", fmt.Errorf("kind %s is not supported for apply: %s", kind, reason)
	}

	ep, ok := kindEndpoint[kind]
	if !ok {
		return "", fmt.Errorf("unknown kind: %s", kind)
	}

	return ep, nil
}

// ExistsResult holds the result of an existence check.
type ExistsResult struct {
	Exists bool
	ID     string
}

// Exists checks whether a resource of the given kind with the specified workspace+name already exists.
// For Workspace kind, only name is used for lookup.
func (s *GenericService) Exists(kind, workspace, name string) (*ExistsResult, error) {
	ep, err := endpointForKind(kind)
	if err != nil {
		return nil, err
	}

	rs := newResourceService(s.client, ep, kind)

	params := url.Values{}
	params.Add("metadata->>name", "eq."+name)

	if kind != "Workspace" && workspace != "" {
		params.Add("metadata->>workspace", "eq."+workspace)
	}

	var items []json.RawMessage
	if err := rs.list(params, &items); err != nil {
		return nil, fmt.Errorf("failed to check existence of %s %s: %w", kind, name, err)
	}

	if len(items) == 0 {
		return &ExistsResult{Exists: false}, nil
	}

	// Extract the ID from the first matching item
	var idHolder struct {
		ID int `json:"id"`
	}

	if err := json.Unmarshal(items[0], &idHolder); err != nil {
		return nil, fmt.Errorf("failed to extract ID from %s %s: %w", kind, name, err)
	}

	return &ExistsResult{
		Exists: true,
		ID:     fmt.Sprintf("%d", idHolder.ID),
	}, nil
}

// Create creates a new resource of the given kind.
func (s *GenericService) Create(kind string, data any) error {
	ep, err := endpointForKind(kind)
	if err != nil {
		return err
	}

	rs := newResourceService(s.client, ep, kind)

	return rs.create(data)
}

// Update updates an existing resource of the given kind by ID.
func (s *GenericService) Update(kind string, id string, data any) error {
	ep, err := endpointForKind(kind)
	if err != nil {
		return err
	}

	rs := newResourceService(s.client, ep, kind)

	return rs.update(id, data)
}

// BuildScheme creates and returns a scheme with all v1 types registered.
func BuildScheme() (*scheme.Scheme, error) {
	s := scheme.NewScheme()
	if err := v1.AddToScheme(s); err != nil {
		return nil, fmt.Errorf("failed to register v1 types: %w", err)
	}

	return s, nil
}
