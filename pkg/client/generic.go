package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/scheme"
)

// NotFoundError is returned when a resource does not exist.
type NotFoundError struct {
	Kind string
	Name string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("%s %q not found", e.Kind, e.Name)
}

// IsNotFound returns true if the error indicates a resource was not found.
func IsNotFound(err error) bool {
	var nfe *NotFoundError
	return errors.As(err, &nfe)
}

const kindWorkspace = "Workspace"

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

// unsupportedKinds are kinds that cannot be created via the REST API.
var unsupportedKinds = map[string]string{
	"ApiKey":      "ApiKey only supports GET/PATCH, not POST creation",
	"UserProfile": "UserProfile is created through the auth system, not the REST API",
}

func (s *GenericService) endpointForKind(kind string) (string, error) {
	if reason, ok := unsupportedKinds[kind]; ok {
		return "", fmt.Errorf("kind %s is not supported for apply: %s", kind, reason)
	}

	ep, ok := s.scheme.KindToTable(kind)
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
	ep, err := s.endpointForKind(kind)
	if err != nil {
		return nil, err
	}

	rs := newResourceService(s.client, ep, kind)

	params := url.Values{}
	params.Add("metadata->>name", "eq."+name)

	if kind != kindWorkspace && workspace != "" {
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
	ep, err := s.endpointForKind(kind)
	if err != nil {
		return err
	}

	rs := newResourceService(s.client, ep, kind)

	return rs.create(data)
}

// Update updates an existing resource of the given kind by ID.
func (s *GenericService) Update(kind string, id string, data any) error {
	ep, err := s.endpointForKind(kind)
	if err != nil {
		return err
	}

	rs := newResourceService(s.client, ep, kind)

	return rs.update(id, data)
}

// DeleteOptions configures the soft-delete request.
type DeleteOptions struct {
	Force bool // set neutree.ai/force-delete annotation
}

// Delete soft-deletes a resource of the given kind by ID.
// It first GETs the resource to retrieve its full metadata, then sends a PATCH
// setting metadata.deletion_timestamp. The full metadata is needed because
// PostgREST replaces the entire composite field on PATCH.
func (s *GenericService) Delete(kind, id, workspace, name string, opts DeleteOptions) error {
	ep, err := s.endpointForKind(kind)
	if err != nil {
		return err
	}

	// GET current resource to retrieve full metadata for backfill
	data, err := s.Get(kind, workspace, name)
	if err != nil {
		return fmt.Errorf("failed to get %s %s before delete: %w", kind, name, err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("failed to parse %s %s: %w", kind, name, err)
	}

	var meta map[string]any
	if metaRaw, ok := raw["metadata"]; ok {
		if err := json.Unmarshal(metaRaw, &meta); err != nil {
			return fmt.Errorf("failed to parse metadata of %s %s: %w", kind, name, err)
		}
	} else {
		meta = map[string]any{}
	}

	if opts.Force {
		annotations, _ := meta["annotations"].(map[string]any)
		if annotations == nil {
			annotations = map[string]any{}
		}

		annotations["neutree.ai/force-delete"] = "true"
		meta["annotations"] = annotations
	}

	meta["deletion_timestamp"] = time.Now().UTC().Format(time.RFC3339)

	payload := map[string]any{
		"metadata": meta,
	}

	rs := newResourceService(s.client, ep, kind)

	return rs.update(id, payload)
}

// ResolveKind resolves a user input string (case-insensitive, singular or plural)
// to the canonical kind name. For example: "endpoint", "Endpoint", "endpoints" all → "Endpoint".
func (s *GenericService) ResolveKind(input string) (string, error) {
	kind, ok := s.scheme.ResolveKind(input)
	if !ok {
		return "", fmt.Errorf("unknown kind: %s", input)
	}

	return kind, nil
}

// readEndpointForKind returns the table endpoint for a kind without checking unsupportedKinds
// (read operations are allowed for all kinds).
func (s *GenericService) readEndpointForKind(kind string) (string, error) {
	ep, ok := s.scheme.KindToTable(kind)
	if !ok {
		return "", fmt.Errorf("unknown kind: %s", kind)
	}

	return ep, nil
}

// List retrieves all resources of the given kind, optionally filtered by workspace.
func (s *GenericService) List(kind, workspace string) ([]json.RawMessage, error) {
	ep, err := s.readEndpointForKind(kind)
	if err != nil {
		return nil, err
	}

	rs := newResourceService(s.client, ep, kind)

	params := url.Values{}
	if kind != kindWorkspace && workspace != "" {
		params.Add("metadata->>workspace", "eq."+workspace)
	}

	var items []json.RawMessage
	if err := rs.list(params, &items); err != nil {
		return nil, fmt.Errorf("failed to list %s: %w", kind, err)
	}

	return items, nil
}

// Get retrieves a single resource of the given kind by workspace and name.
func (s *GenericService) Get(kind, workspace, name string) (json.RawMessage, error) {
	ep, err := s.readEndpointForKind(kind)
	if err != nil {
		return nil, err
	}

	rs := newResourceService(s.client, ep, kind)

	params := url.Values{}
	params.Add("metadata->>name", "eq."+name)

	if kind != kindWorkspace && workspace != "" {
		params.Add("metadata->>workspace", "eq."+workspace)
	}

	var items []json.RawMessage
	if err := rs.list(params, &items); err != nil {
		return nil, fmt.Errorf("failed to get %s %s: %w", kind, name, err)
	}

	if len(items) == 0 {
		return nil, &NotFoundError{Kind: kind, Name: name}
	}

	return items[0], nil
}

// ExtractPhase extracts the status.phase field from raw JSON resource data.
func ExtractPhase(data json.RawMessage) string {
	var holder struct {
		Status struct {
			Phase string `json:"phase"`
		} `json:"status"`
	}

	if err := json.Unmarshal(data, &holder); err != nil {
		return ""
	}

	return holder.Status.Phase
}

// ExtractMetadataField extracts a field from metadata in raw JSON resource data.
func ExtractMetadataField(data json.RawMessage, field string) string {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return ""
	}

	metaRaw, ok := raw["metadata"]
	if !ok {
		return ""
	}

	var meta map[string]json.RawMessage
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		return ""
	}

	valRaw, ok := meta[field]
	if !ok {
		return ""
	}

	var val string
	if err := json.Unmarshal(valRaw, &val); err != nil {
		return ""
	}

	return val
}

// BuildScheme creates and returns a scheme with all v1 types registered.
func BuildScheme() (*scheme.Scheme, error) {
	s := scheme.NewScheme()
	if err := v1.AddToScheme(s); err != nil {
		return nil, fmt.Errorf("failed to register v1 types: %w", err)
	}

	return s, nil
}
