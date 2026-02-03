package v1beta1

import (
	"strconv"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/scheme"
)

type ExternalEndpointUpstreamSpec struct {
	// URL is the full base URL of the external API
	// Example: "https://api.openai.com/v1/chat/completions"
	URL string `json:"url"`
}

type ExternalEndpointAuthSpec struct {
	// Type is the authentication type: "bearer" or "api_key"
	// bearer: Authorization: Bearer {credential}
	// api_key: Authorization: {credential}
	Type string `json:"type"`

	// Credential is the API Key or Token (stored in plain text, hidden in API response)
	Credential string `json:"credential" api:"-"`
}

type ExternalEndpointSpec struct {
	// Upstream is the external API configuration
	Upstream *ExternalEndpointUpstreamSpec `json:"upstream"`

	// Auth is the external API authentication configuration (optional)
	Auth *ExternalEndpointAuthSpec `json:"auth,omitempty"`

	// RouteType is the AI statistics type: "/v1/chat/completions", "/v1/embeddings", "/v1/rerank"
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
	ID         int                       `json:"id,omitempty"`
	APIVersion string                    `json:"api_version,omitempty"`
	Kind       string                    `json:"kind,omitempty"`
	Metadata   *v1.Metadata              `json:"metadata,omitempty"`
	Spec       *ExternalEndpointSpec     `json:"spec,omitempty"`
	Status     *ExternalEndpointStatus   `json:"status,omitempty"`
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
		obj.Metadata = &v1.Metadata{}
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
		obj.Metadata = &v1.Metadata{}
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
