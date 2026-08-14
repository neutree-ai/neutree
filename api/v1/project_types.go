package v1

import "github.com/neutree-ai/neutree/pkg/scheme"

// Project groups API keys in one workspace. Projects are deliberately a
// control-plane-only resource: changing one never changes gateway credentials.
type ProjectSpec struct {
	Description string `json:"description,omitempty"`
	Disabled    bool   `json:"disabled,omitempty"`
	Default     bool   `json:"default,omitempty"`
}

type ProjectStatus struct {
	Phase        string `json:"phase,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
}

type Project struct {
	ID         string         `json:"id,omitempty"`
	APIVersion string         `json:"api_version,omitempty"`
	Kind       string         `json:"kind,omitempty"`
	Metadata   *Metadata      `json:"metadata,omitempty"`
	Spec       *ProjectSpec   `json:"spec,omitempty"`
	Status     *ProjectStatus `json:"status,omitempty"`
}

func (obj *Project) GetName() string { if obj.Metadata == nil { return "" }; return obj.Metadata.Name }
func (obj *Project) GetWorkspace() string { if obj.Metadata == nil { return "" }; return obj.Metadata.Workspace }
func (obj *Project) GetLabels() map[string]string { if obj.Metadata == nil { return nil }; return obj.Metadata.Labels }
func (obj *Project) SetLabels(labels map[string]string) { if obj.Metadata == nil { obj.Metadata = &Metadata{} }; obj.Metadata.Labels = labels }
func (obj *Project) GetAnnotations() map[string]string { if obj.Metadata == nil { return nil }; return obj.Metadata.Annotations }
func (obj *Project) SetAnnotations(annotations map[string]string) { if obj.Metadata == nil { obj.Metadata = &Metadata{} }; obj.Metadata.Annotations = annotations }
func (obj *Project) GetCreationTimestamp() string { if obj.Metadata == nil { return "" }; return obj.Metadata.CreationTimestamp }
func (obj *Project) GetUpdateTimestamp() string { if obj.Metadata == nil { return "" }; return obj.Metadata.UpdateTimestamp }
func (obj *Project) GetDeletionTimestamp() string { if obj.Metadata == nil { return "" }; return obj.Metadata.DeletionTimestamp }
func (obj *Project) GetSpec() interface{} { return obj.Spec }
func (obj *Project) GetStatus() interface{} { return obj.Status }
func (obj *Project) GetKind() string { return obj.Kind }
func (obj *Project) SetKind(kind string) { obj.Kind = kind }
func (obj *Project) GetID() string { return obj.ID }
func (obj *Project) GetMetadata() interface{} { return obj.Metadata }

type ProjectList struct { Kind string `json:"kind"`; Items []Project `json:"items"` }
func (in *ProjectList) GetKind() string { return in.Kind }
func (in *ProjectList) SetKind(kind string) { in.Kind = kind }
func (in *ProjectList) GetItems() []scheme.Object { objs := make([]scheme.Object, 0, len(in.Items)); for i := range in.Items { objs = append(objs, &in.Items[i]) }; return objs }
func (in *ProjectList) SetItems(objs []scheme.Object) { in.Items = make([]Project, len(objs)); for i, obj := range objs { in.Items[i] = *obj.(*Project) } }
