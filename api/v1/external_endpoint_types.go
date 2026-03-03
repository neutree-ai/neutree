package v1

import (
	"strconv"

	"github.com/neutree-ai/neutree/pkg/scheme"
)

type ExternalEndpointUpstreamSpec struct {
	// URL is the base URL of the external API (without API path suffix)
	// Example: "https://api.openai.com"
	URL string `json:"url"`
}

const (
	ExternalEndpointAuthTypeBearer = "bearer"
	ExternalEndpointAuthTypeAPIKey = "api_key"
)

type ExternalEndpointAuthSpec struct {
	// Type is the authentication type: "bearer" or "api_key"
	// bearer: Authorization: Bearer {credential}
	// api_key: Authorization: {credential}
	Type string `json:"type"`

	// Credential is the API Key or Token (stored in plain text, hidden in API response)
	Credential string `json:"credential" api:"-"`
}

// AuthHeaderValue returns the formatted Authorization header value
func (a *ExternalEndpointAuthSpec) AuthHeaderValue() string {
	if a.Type == ExternalEndpointAuthTypeBearer {
		return "Bearer " + a.Credential
	}

	return a.Credential
}

type ExternalEndpointUpstreamEntry struct {
	// Upstream is the external API configuration (for external upstream type)
	Upstream *ExternalEndpointUpstreamSpec `json:"upstream,omitempty"`

	// EndpointRef is the name of an Internal Endpoint in the same workspace (for endpoint ref type)
	EndpointRef *string `json:"endpoint_ref,omitempty"`

	// Auth is the authentication configuration for this entry (only for external upstream type)
	Auth *ExternalEndpointAuthSpec `json:"auth,omitempty"`

	// ModelMapping maps client-facing model names to upstream model names
	// The keys are the exposed model names, values are the upstream model names
	// e.g. {"fast": "gpt-4o-mini"} exposes "fast" and forwards as "gpt-4o-mini"
	ModelMapping map[string]string `json:"model_mapping"`
}

// Route type constants for AI statistics
const (
	RouteTypeChatCompletions = "/v1/chat/completions"
	RouteTypeEmbeddings      = "/v1/embeddings"
	RouteTypeRerank          = "/v1/rerank"
)

type ExternalEndpointSpec struct {
	// Upstreams is the list of upstream entries
	Upstreams []ExternalEndpointUpstreamEntry `json:"upstreams"`

	// RouteType is the AI statistics route type
	RouteType string `json:"route_type"`

	// Timeout is the request timeout in milliseconds, default 60000
	Timeout *int `json:"timeout,omitempty"`
}

type ExternalEndpointPhase string

const (
	ExternalEndpointPhasePENDING ExternalEndpointPhase = "Pending"
	ExternalEndpointPhaseRUNNING ExternalEndpointPhase = "Running"
	ExternalEndpointPhaseFAILED  ExternalEndpointPhase = "Failed"
	ExternalEndpointPhaseDELETED ExternalEndpointPhase = "Deleted"
)

type ExternalEndpointStatus struct {
	Phase              ExternalEndpointPhase `json:"phase,omitempty"`
	ServiceURL         string                `json:"service_url,omitempty"`
	LastTransitionTime string                `json:"last_transition_time,omitempty"`
	ErrorMessage       string                `json:"error_message,omitempty"`
}

type ExternalEndpoint struct {
	ID         int                     `json:"id,omitempty"`
	APIVersion string                  `json:"api_version,omitempty"`
	Kind       string                  `json:"kind,omitempty"`
	Metadata   *Metadata               `json:"metadata,omitempty"`
	Spec       *ExternalEndpointSpec   `json:"spec,omitempty"`
	Status     *ExternalEndpointStatus `json:"status,omitempty"`
}

func (e ExternalEndpoint) Key() string {
	if e.Metadata == nil {
		return "default" + "-" + "external-endpoint" + "-" + strconv.Itoa(e.ID)
	}

	if e.Metadata.Workspace == "" {
		return "default" + "-" + "external-endpoint" + "-" + strconv.Itoa(e.ID) + "-" + e.Metadata.Name
	}

	return e.Metadata.Workspace + "-" + "external-endpoint" + "-" + strconv.Itoa(e.ID) + "-" + e.Metadata.Name
}

func (obj *ExternalEndpoint) GetName() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Name
}

func (obj *ExternalEndpoint) GetWorkspace() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Workspace
}

func (obj *ExternalEndpoint) GetLabels() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Labels
}

func (obj *ExternalEndpoint) SetLabels(labels map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Labels = labels
}

func (obj *ExternalEndpoint) GetAnnotations() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Annotations
}

func (obj *ExternalEndpoint) SetAnnotations(annotations map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Annotations = annotations
}

func (obj *ExternalEndpoint) GetCreationTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.CreationTimestamp
}

func (obj *ExternalEndpoint) GetUpdateTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.UpdateTimestamp
}

func (obj *ExternalEndpoint) GetDeletionTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.DeletionTimestamp
}

func (obj *ExternalEndpoint) GetSpec() interface{} {
	return obj.Spec
}

func (obj *ExternalEndpoint) GetStatus() interface{} {
	return obj.Status
}

func (obj *ExternalEndpoint) GetKind() string {
	return obj.Kind
}

func (obj *ExternalEndpoint) SetKind(kind string) {
	obj.Kind = kind
}

func (obj *ExternalEndpoint) GetID() string {
	return strconv.Itoa(obj.ID)
}

func (obj *ExternalEndpoint) SetID(id string) {
	obj.ID, _ = strconv.Atoi(id)
}

func (obj *ExternalEndpoint) GetMetadata() interface{} {
	return obj.Metadata
}

// ExternalEndpointList is a list of ExternalEndpoint resources
type ExternalEndpointList struct {
	Kind  string             `json:"kind"`
	Items []ExternalEndpoint `json:"items"`
}

func (in *ExternalEndpointList) GetKind() string {
	return in.Kind
}

func (in *ExternalEndpointList) SetKind(kind string) {
	in.Kind = kind
}

func (in *ExternalEndpointList) GetItems() []scheme.Object {
	var objs []scheme.Object
	for i := range in.Items {
		objs = append(objs, &in.Items[i])
	}

	return objs
}

func (in *ExternalEndpointList) SetItems(objs []scheme.Object) {
	items := make([]ExternalEndpoint, len(objs))
	for i, obj := range objs {
		items[i] = *obj.(*ExternalEndpoint) //nolint:errcheck
	}

	in.Items = items
}
