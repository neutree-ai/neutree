package v1

import "github.com/neutree-ai/neutree/pkg/scheme"

// ApiKeyProjectSpec contains the user-managed fields of an API key folder.
// Ownership remains a platform-managed column on ApiKeyProject.
type ApiKeyProjectSpec struct {
	Description string `json:"description,omitempty"`
}

type ApiKeyProject struct {
	ID         string             `json:"id,omitempty"`
	APIVersion string             `json:"api_version,omitempty"`
	Kind       string             `json:"kind,omitempty"`
	Metadata   *Metadata          `json:"metadata,omitempty"`
	Spec       *ApiKeyProjectSpec `json:"spec,omitempty"`
	UserID     string             `json:"user_id,omitempty"`
}

func (obj *ApiKeyProject) GetName() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Name
}

func (obj *ApiKeyProject) GetWorkspace() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Workspace
}

func (obj *ApiKeyProject) GetLabels() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Labels
}

func (obj *ApiKeyProject) SetLabels(labels map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Labels = labels
}

func (obj *ApiKeyProject) GetAnnotations() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Annotations
}

func (obj *ApiKeyProject) SetAnnotations(annotations map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Annotations = annotations
}

func (obj *ApiKeyProject) GetCreationTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.CreationTimestamp
}

func (obj *ApiKeyProject) GetUpdateTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.UpdateTimestamp
}

func (obj *ApiKeyProject) GetDeletionTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.DeletionTimestamp
}

func (obj *ApiKeyProject) GetSpec() interface{}     { return obj.Spec }
func (obj *ApiKeyProject) GetStatus() interface{}   { return nil }
func (obj *ApiKeyProject) GetKind() string          { return obj.Kind }
func (obj *ApiKeyProject) SetKind(kind string)      { obj.Kind = kind }
func (obj *ApiKeyProject) GetID() string            { return obj.ID }
func (obj *ApiKeyProject) GetMetadata() interface{} { return obj.Metadata }

type ApiKeyProjectList struct {
	Kind  string          `json:"kind"`
	Items []ApiKeyProject `json:"items"`
}

func (in *ApiKeyProjectList) GetKind() string     { return in.Kind }
func (in *ApiKeyProjectList) SetKind(kind string) { in.Kind = kind }
func (in *ApiKeyProjectList) GetItems() []scheme.Object {
	items := make([]scheme.Object, len(in.Items))
	for i := range in.Items {
		items[i] = &in.Items[i]
	}

	return items
}
func (in *ApiKeyProjectList) SetItems(objs []scheme.Object) {
	in.Items = make([]ApiKeyProject, len(objs))
	for i, obj := range objs {
		in.Items[i] = *obj.(*ApiKeyProject) //nolint:errcheck
	}
}
