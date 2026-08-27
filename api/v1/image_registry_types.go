package v1

import (
	"strconv"

	"github.com/neutree-ai/neutree/pkg/scheme"
)

type ImageRegistryPhase string

const (
	ImageRegistryPhasePENDING   ImageRegistryPhase = "Pending"
	ImageRegistryPhaseCONNECTED ImageRegistryPhase = "Connected"
	ImageRegistryPhaseFAILED    ImageRegistryPhase = "Failed"
	ImageRegistryPhaseDELETED   ImageRegistryPhase = "Deleted"
)

type ImageRegistry struct {
	APIVersion string               `json:"api_version,omitempty"`
	ID         int                  `json:"id,omitempty"`
	Kind       string               `json:"kind,omitempty"`
	Metadata   *Metadata            `json:"metadata,omitempty"`
	Spec       *ImageRegistrySpec   `json:"spec,omitempty"`
	Status     *ImageRegistryStatus `json:"status,omitempty"`
}

type ImageRegistryAuthConfig struct {
	Password      string `json:"password,omitempty"`
	Username      string `json:"username,omitempty"`
	Auth          string `json:"auth,omitempty"`
	IdentityToken string `json:"identitytoken,omitempty"`
	RegistryToken string `json:"registrytoken,omitempty"`
}

type ImageRegistrySpec struct {
	AuthConfig ImageRegistryAuthConfig `json:"authconfig" api:"-"`
	Repository string                  `json:"repository"`
	URL        string                  `json:"url"`
}

type ImageRegistryStatus struct {
	ErrorMessage       string             `json:"error_message,omitempty"`
	LastTransitionTime string             `json:"last_transition_time,omitempty"`
	Phase              ImageRegistryPhase `json:"phase,omitempty"`
	// Capabilities records what this registry turned out to be able to do,
	// established while connecting to it. It is a cache of an observation, not
	// a contract: credentials get rotated and permissions get changed, so a
	// caller still has to handle the call failing anyway.
	Capabilities *ImageRegistryCapabilities `json:"capabilities,omitempty"`
}

// ListRepositoriesCapability says how -- or whether -- this registry's
// repositories can be enumerated.
//
// There is no portable way to do it. The OCI distribution spec has no endpoint
// for it at all: its only listing is end-8a/8b, GET /v2/<name>/tags/list, which
// needs the repository name you are trying to discover. The /v2/_catalog that
// registries inherited from Docker Registry v2 is not in the spec, and where it
// exists it is locked: Docker Hub's token service issues no catalog scope to
// anyone, and Harbor reserves it for system administrators. So each registry
// gets asked in its own dialect, and the ones with no dialect say so.
type ListRepositoriesCapability string

const (
	// ListRepositoriesHarborProjects -- Harbor's own API lists the repositories
	// in a project, with paging and search, for a credential that can read the
	// project.
	ListRepositoriesHarborProjects ListRepositoriesCapability = "harbor-projects"
	// ListRepositoriesNamespaceRequired -- Docker Hub lists the repositories in
	// a namespace, but has no endpoint that enumerates namespaces. The
	// namespace has to come from the user; there is no way around it, and a
	// built-in list of namespaces would be a hand-maintained inventory rather
	// than an answer.
	ListRepositoriesNamespaceRequired ListRepositoriesCapability = "namespace-required"
	// ListRepositoriesUnauthorized -- the registry can do it, and these
	// credentials cannot. A wider credential fixes it; retrying does not.
	ListRepositoriesUnauthorized ListRepositoriesCapability = "unauthorized"
	// ListRepositoriesUnsupported -- nothing here knows how to ask this
	// registry.
	ListRepositoriesUnsupported ListRepositoriesCapability = "unsupported"
)

// ImageRegistryCapabilities is what was established about a registry beyond
// whether it could be reached.
type ImageRegistryCapabilities struct {
	ListRepositories ListRepositoriesCapability `json:"list_repositories,omitempty"`
}

// GetCapabilities reports what was last established about a registry, or nil
// when nothing has been.
func (in *ImageRegistryStatus) GetCapabilities() *ImageRegistryCapabilities {
	if in == nil {
		return nil
	}

	return in.Capabilities
}

// GetListRepositoriesCapability reports how this registry can be enumerated, or
// empty when that has never been established.
func (obj *ImageRegistry) GetListRepositoriesCapability() ListRepositoriesCapability {
	capabilities := obj.Status.GetCapabilities()
	if capabilities == nil {
		return ""
	}

	return capabilities.ListRepositories
}

func (obj *ImageRegistry) GetName() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Name
}

func (obj *ImageRegistry) GetWorkspace() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Workspace
}

func (obj *ImageRegistry) GetLabels() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Labels
}

func (obj *ImageRegistry) SetLabels(labels map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Labels = labels
}

func (obj *ImageRegistry) GetAnnotations() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Annotations
}

func (obj *ImageRegistry) SetAnnotations(annotations map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Annotations = annotations
}

func (obj *ImageRegistry) GetCreationTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.CreationTimestamp
}

func (obj *ImageRegistry) GetUpdateTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.UpdateTimestamp
}

func (obj *ImageRegistry) GetDeletionTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.DeletionTimestamp
}

func (obj *ImageRegistry) GetSpec() interface{} {
	return obj.Spec
}

func (obj *ImageRegistry) GetStatus() interface{} {
	return obj.Status
}

func (obj *ImageRegistry) GetKind() string {
	return obj.Kind
}

func (obj *ImageRegistry) SetKind(kind string) {
	obj.Kind = kind
}

func (obj *ImageRegistry) GetID() string {
	return strconv.Itoa(obj.ID)
}

func (obj *ImageRegistry) SetID(id string) {
	obj.ID, _ = strconv.Atoi(id)
}

func (obj *ImageRegistry) GetMetadata() interface{} {
	return obj.Metadata
}

// ImageRegistryList is a list of ImageRegistry resources
type ImageRegistryList struct {
	Kind  string          `json:"kind"`
	Items []ImageRegistry `json:"items"`
}

func (in *ImageRegistryList) GetKind() string {
	return in.Kind
}

func (in *ImageRegistryList) SetKind(kind string) {
	in.Kind = kind
}

func (in *ImageRegistryList) GetItems() []scheme.Object {
	var objs []scheme.Object
	for i := range in.Items {
		objs = append(objs, &in.Items[i])
	}

	return objs
}

func (in *ImageRegistryList) SetItems(objs []scheme.Object) {
	items := make([]ImageRegistry, len(objs))
	for i, obj := range objs {
		items[i] = *obj.(*ImageRegistry) //nolint:errcheck
	}

	in.Items = items
}
