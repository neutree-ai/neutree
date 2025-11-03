package v1

import "github.com/neutree-ai/neutree/pkg/scheme"

type UserProfileSpec struct {
	Email string `json:"email"`
}

type UserProfileStatus struct {
	Phase        string `json:"phase,omitempty"`
	ServiceURL   string `json:"service_url,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
}

type UserProfile struct {
	ID         string             `json:"id,omitempty"` // UUID
	APIVersion string             `json:"api_version,omitempty"`
	Kind       string             `json:"kind,omitempty"`
	Metadata   *Metadata          `json:"metadata,omitempty"`
	Spec       *UserProfileSpec   `json:"spec,omitempty"`
	Status     *UserProfileStatus `json:"status,omitempty"`
}

func (obj *UserProfile) GetName() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Name
}

func (obj *UserProfile) GetWorkspace() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Workspace
}

func (obj *UserProfile) GetLabels() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Labels
}

func (obj *UserProfile) SetLabels(labels map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Labels = labels
}

func (obj *UserProfile) GetAnnotations() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Annotations
}

func (obj *UserProfile) SetAnnotations(annotations map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Annotations = annotations
}

func (obj *UserProfile) GetCreationTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.CreationTimestamp
}

func (obj *UserProfile) GetUpdateTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.UpdateTimestamp
}

func (obj *UserProfile) GetDeletionTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.DeletionTimestamp
}

func (obj *UserProfile) GetSpec() interface{} {
	return obj.Spec
}

func (obj *UserProfile) GetStatus() interface{} {
	return obj.Status
}

func (obj *UserProfile) GetKind() string {
	return obj.Kind
}

func (obj *UserProfile) SetKind(kind string) {
	obj.Kind = kind
}

func (obj *UserProfile) GetID() string {
	return obj.ID
}

func (obj *UserProfile) GetMetadata() interface{} {
	return obj.Metadata
}

// UserProfileList is a list of UserProfile resources
type UserProfileList struct {
	Kind  string        `json:"kind"`
	Items []UserProfile `json:"items"`
}

func (in *UserProfileList) GetKind() string {
	return in.Kind
}

func (in *UserProfileList) SetKind(kind string) {
	in.Kind = kind
}

func (in *UserProfileList) GetItems() []scheme.Object {
	var objs []scheme.Object
	for i := range in.Items {
		objs = append(objs, &in.Items[i])
	}

	return objs
}

func (in *UserProfileList) SetItems(objs []scheme.Object) {
	items := make([]UserProfile, len(objs))
	for i, obj := range objs {
		items[i] = *obj.(*UserProfile) //nolint:errcheck
	}

	in.Items = items
}
