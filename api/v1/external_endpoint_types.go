package v1

import (
	"net/url"
	"sort"
	"strconv"

	"github.com/neutree-ai/neutree/pkg/scheme"
)

type ExternalEndpointUpstreamSpec struct {
	// URL is the base URL of the external API, including the API version path.
	// Example: "https://api.openai.com/v1"
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
	// Name identifies this provider entry for model_routes targets. It is
	// optional for the legacy model_mapping format.
	Name string `json:"name,omitempty"`

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

// ExternalEndpointModelRouteTarget binds one virtual model to one concrete
// upstream model. Routing policy belongs here, rather than on the provider,
// so the same provider can be used with different policies by different
// virtual models.
type ExternalEndpointModelRouteTarget struct {
	Upstream            string `json:"upstream"`
	UpstreamModel       string `json:"upstream_model"`
	Priority            int    `json:"priority,omitempty"`
	Weight              int    `json:"weight,omitempty"`
	MaxInflightRequests int    `json:"max_inflight_requests,omitempty"`
}

type ExternalEndpointModelRoute struct {
	Model    string                             `json:"model"`
	Strategy string                             `json:"strategy,omitempty"`
	Targets  []ExternalEndpointModelRouteTarget `json:"targets"`
}

// Upstream entry kinds, used to describe an entry in the status without
// leaking its credential.
const (
	ExternalEndpointUpstreamKindEndpointRef = "endpoint_ref"
	ExternalEndpointUpstreamKindExternal    = "external"
)

// Kind reports whether the entry points at an internal endpoint or an external URL.
func (e *ExternalEndpointUpstreamEntry) Kind() string {
	if e.EndpointRef != nil {
		return ExternalEndpointUpstreamKindEndpointRef
	}

	return ExternalEndpointUpstreamKindExternal
}

// Ref returns the stable identity of the entry: the referenced internal endpoint
// name, or the external upstream URL with anything credential-bearing removed.
// It is safe to publish in status, logs and error messages.
func (e *ExternalEndpointUpstreamEntry) Ref() string {
	if e.EndpointRef != nil {
		return *e.EndpointRef
	}

	if e.Upstream != nil {
		return SanitizeUpstreamURL(e.Upstream.URL)
	}

	return ""
}

// unparsableUpstreamURL stands in for a URL we could not parse. Echoing the raw
// value would risk leaking whatever it embeds.
const unparsableUpstreamURL = "(unparsable upstream url)"

// SanitizeUpstreamURL strips the credential-bearing parts of an upstream URL:
// the userinfo component (https://user:pass@host) and the query string, which
// providers commonly use to carry an API key (?api-key=...). The result is a
// deterministic function of the URL, so it still identifies the entry it came
// from — two upstreams differing only in their query string collapse to the
// same reference, which is the intended trade for not publishing secrets.
func SanitizeUpstreamURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return unparsableUpstreamURL
	}

	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""

	return u.String()
}

// ExposedModels returns the client-facing model names this entry serves, sorted
// so the status stays stable across reconciles (Go map iteration is random).
func (e *ExternalEndpointUpstreamEntry) ExposedModels() []string {
	if len(e.ModelMapping) == 0 {
		return nil
	}

	models := make([]string, 0, len(e.ModelMapping))
	for exposed := range e.ModelMapping {
		models = append(models, exposed)
	}

	sort.Strings(models)

	return models
}

// Route type constants for AI statistics
const (
	RouteTypeChatCompletions = "/v1/chat/completions"
	RouteTypeEmbeddings      = "/v1/embeddings"
	RouteTypeRerank          = "/v1/rerank"
)

type ExternalEndpointSpec struct {
	// Upstreams is the list of upstream entries.
	// The mergekey tag lets the API proxy backfill each entry's masked
	// auth.credential from the stored entry with the same identity (upstream.url
	// or endpoint_ref) instead of by array index, so deleting or reordering
	// entries cannot leak one upstream's credential into another.
	Upstreams []ExternalEndpointUpstreamEntry `json:"upstreams" mergekey:"upstream.url,endpoint_ref"`

	// ModelRoutes is the model-scoped routing format. When present, the
	// gateway uses these routes instead of the legacy upstream model_mapping.
	ModelRoutes []ExternalEndpointModelRoute `json:"model_routes,omitempty"`

	// Timeout is the request timeout in milliseconds, default 60000
	Timeout *int `json:"timeout,omitempty"`
}

type ExternalEndpointPhase string

const (
	ExternalEndpointPhasePENDING ExternalEndpointPhase = "Pending"
	ExternalEndpointPhaseRUNNING ExternalEndpointPhase = "Running"
	// ExternalEndpointPhaseDEGRADED means the endpoint is serving, but at least
	// one upstream could not be resolved and was left out of the gateway
	// configuration. Models served only by that upstream are unavailable; every
	// other model keeps working. Per-upstream detail is in UpstreamStatuses.
	ExternalEndpointPhaseDEGRADED ExternalEndpointPhase = "Degraded"
	ExternalEndpointPhaseFAILED   ExternalEndpointPhase = "Failed"
	ExternalEndpointPhaseDELETED  ExternalEndpointPhase = "Deleted"
)

type ExternalEndpointUpstreamPhase string

const (
	ExternalEndpointUpstreamPhaseReady  ExternalEndpointUpstreamPhase = "Ready"
	ExternalEndpointUpstreamPhaseFailed ExternalEndpointUpstreamPhase = "Failed"
)

// ExternalEndpointUpstreamStatus reports the outcome of resolving a single
// upstream entry. One entry is emitted per spec upstream, in spec order.
type ExternalEndpointUpstreamStatus struct {
	// Kind is "endpoint_ref" or "external".
	Kind string `json:"kind,omitempty"`

	// Ref is the referenced internal endpoint name or the external upstream URL.
	// It never carries the auth credential.
	Ref string `json:"ref,omitempty"`

	// Models are the client-facing model names this upstream serves. When the
	// upstream is Failed, these are exactly the models that stopped resolving.
	Models []string `json:"models,omitempty"`

	Phase        ExternalEndpointUpstreamPhase `json:"phase,omitempty"`
	ErrorMessage string                        `json:"error_message,omitempty"`
}

type ExternalEndpointStatus struct {
	Phase              ExternalEndpointPhase `json:"phase,omitempty"`
	ServiceURL         string                `json:"service_url,omitempty"`
	LastTransitionTime string                `json:"last_transition_time,omitempty"`
	ErrorMessage       string                `json:"error_message,omitempty"`

	// UpstreamStatuses carries per-upstream detail so a single broken upstream is
	// distinguishable from a wholly broken endpoint.
	UpstreamStatuses []ExternalEndpointUpstreamStatus `json:"upstream_status,omitempty"`
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
