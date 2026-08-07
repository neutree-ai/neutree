package v1

import (
	"strconv"

	"github.com/neutree-ai/neutree/pkg/scheme"
)

type ModelRegistryPhase string

const (
	ModelRegistryPhasePENDING   ModelRegistryPhase = "Pending"
	ModelRegistryPhaseCONNECTED ModelRegistryPhase = "Connected"
	ModelRegistryPhaseFAILED    ModelRegistryPhase = "Failed"
	ModelRegistryPhaseDELETED   ModelRegistryPhase = "Deleted"
)

type ModelRegistryType string

const (
	HuggingFaceModelRegistryType = "hugging-face"
	BentoMLModelRegistryType     = "bentoml"
)

// A registry is either public — a hosted catalogue someone else operates, which
// we can only read and cannot measure — or private, backed by storage this
// deployment owns. Nearly everything a caller wants to vary follows from that
// distinction rather than from the registry kind itself, so the kind is mapped
// to it in exactly one place: here, and in the api.visibility computed column
// that mirrors this function for clients reading the table through PostgREST.
const (
	ModelRegistryVisibilityPublic  = "public"
	ModelRegistryVisibilityPrivate = "private"
)

// VisibilityForModelRegistryType states whether a registry kind is public.
// Unknown kinds are private: a kind this build does not know about is not one
// whose contents are on someone else's hub.
func VisibilityForModelRegistryType(registryType ModelRegistryType) string {
	if registryType == HuggingFaceModelRegistryType {
		return ModelRegistryVisibilityPublic
	}

	return ModelRegistryVisibilityPrivate
}

type BentoMLModelRegistryConnectType string

const (
	BentoMLModelRegistryConnectTypeNFS  = "nfs"
	BentoMLModelRegistryConnectTypeFile = "file"
)

// The environment variable name for model registry
const (
	HFHomeEnv  = "HF_HOME"
	HFTokenEnv = "HF_TOKEN"
	HFEndpoint = "HF_ENDPOINT"

	BentoMLHomeEnv = "BENTOML_HOME"
)

type ModelRegistrySpec struct {
	Type        ModelRegistryType `json:"type"` // only support 'bentoml' | 'hugging-face'
	Url         string            `json:"url"`  // only support 'file://localhost/path/to/model' | 'https://huggingface.co' | 'nfs://nfs-server:/path/to/model';
	Credentials string            `json:"credentials" api:"-"`
}

// ModelRegistryStats is a cached summary of what a registry currently holds. It
// is a projection refreshed out of band, not authoritative data.
type ModelRegistryStats struct {
	// ModelCount is the number of models visible in the registry.
	ModelCount int `json:"model_count"`
	// StorageBytes is the total on-disk size of those models.
	StorageBytes int64 `json:"storage_bytes"`
	// StatsUpdatedAt is when the counters above were last refreshed (RFC3339).
	StatsUpdatedAt string `json:"stats_updated_at,omitempty"`
}

type ModelRegistryStatus struct {
	ErrorMessage string `json:"error_message,omitempty"`
	// LastTransitionTime is when the phase last changed. A registry that has been
	// reachable for three days still reports the moment it first connected, which
	// is why it cannot answer "when was this last checked" — see LastCheckedAt.
	LastTransitionTime string             `json:"last_transition_time,omitempty"`
	Phase              ModelRegistryPhase `json:"phase,omitempty"`
	// LastCheckedAt is when connectivity was last verified, whatever the outcome
	// and whether or not it differed from the previous one. This is the timestamp
	// to show next to a reachability state: it is the one that keeps moving while
	// nothing changes, and the one that goes stale when checking has stopped.
	//
	// It is written back on a throttle rather than on every reconcile, so it
	// trails the real check by up to that interval.
	LastCheckedAt string `json:"last_checked_at,omitempty"`
	// Stats is written by the statistics path, not by the connectivity
	// reconcile. PostgREST replaces a composite-type column as a whole, so every
	// writer of this status has to carry Stats forward or it is nulled out.
	Stats *ModelRegistryStats `json:"stats,omitempty"`
}

type ModelRegistry struct {
	APIVersion string               `json:"api_version,omitempty"`
	ID         int                  `json:"id,omitempty"`
	Kind       string               `json:"kind,omitempty"`
	Metadata   *Metadata            `json:"metadata,omitempty"`
	Spec       *ModelRegistrySpec   `json:"spec,omitempty"`
	Status     *ModelRegistryStatus `json:"status,omitempty"`
	// Visibility is "public" or "private". It is not stored: it is derived from
	// the registry kind by the api.visibility computed column, so every client
	// gets the same answer instead of each re-deriving one from spec.type.
	//
	// PostgREST leaves computed columns out of "select=*", so this is populated
	// only for a caller that asked for it — "?select=*,visibility". It is never
	// accepted on a write, which is why it carries omitempty: nothing here sets
	// it, so it stays out of the bodies this type is marshalled into.
	Visibility string `json:"visibility,omitempty"`
}

func (r ModelRegistry) Key() string {
	if r.Metadata == nil {
		return "default" + "-" + "modelregistry" + "-" + strconv.Itoa(r.ID)
	}

	if r.Metadata.Workspace == "" {
		return "default" + "-" + "modelregistry" + "-" + strconv.Itoa(r.ID) + "-" + r.Metadata.Name
	}

	return r.Metadata.Workspace + "-" + "modelregistry" + "-" + strconv.Itoa(r.ID) + "-" + r.Metadata.Name
}

func (obj *ModelRegistry) GetName() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Name
}

func (obj *ModelRegistry) GetWorkspace() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Workspace
}

func (obj *ModelRegistry) GetLabels() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Labels
}

func (obj *ModelRegistry) SetLabels(labels map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Labels = labels
}

func (obj *ModelRegistry) GetAnnotations() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Annotations
}

func (obj *ModelRegistry) SetAnnotations(annotations map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Annotations = annotations
}

func (obj *ModelRegistry) GetCreationTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.CreationTimestamp
}

func (obj *ModelRegistry) GetUpdateTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.UpdateTimestamp
}

func (obj *ModelRegistry) GetDeletionTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.DeletionTimestamp
}

func (obj *ModelRegistry) GetSpec() interface{} {
	return obj.Spec
}

func (obj *ModelRegistry) GetStatus() interface{} {
	return obj.Status
}

func (obj *ModelRegistry) GetKind() string {
	return obj.Kind
}

func (obj *ModelRegistry) SetKind(kind string) {
	obj.Kind = kind
}

func (obj *ModelRegistry) GetID() string {
	return strconv.Itoa(obj.ID)
}

func (obj *ModelRegistry) SetID(id string) {
	obj.ID, _ = strconv.Atoi(id)
}

func (obj *ModelRegistry) GetMetadata() interface{} {
	return obj.Metadata
}

// ModelRegistryList is a list of ModelRegistry resources
type ModelRegistryList struct {
	Kind  string          `json:"kind"`
	Items []ModelRegistry `json:"items"`
}

func (in *ModelRegistryList) GetKind() string {
	return in.Kind
}

func (in *ModelRegistryList) SetKind(kind string) {
	in.Kind = kind
}

func (in *ModelRegistryList) GetItems() []scheme.Object {
	var objs []scheme.Object
	for i := range in.Items {
		objs = append(objs, &in.Items[i])
	}

	return objs
}

func (in *ModelRegistryList) SetItems(objs []scheme.Object) {
	items := make([]ModelRegistry, len(objs))
	for i, obj := range objs {
		items[i] = *obj.(*ModelRegistry) //nolint:errcheck
	}

	in.Items = items
}
