package v1

import "github.com/neutree-ai/neutree/pkg/scheme"

// Project groups API keys within a workspace.
type Project struct {
	ID          string `json:"id,omitempty"`
	Workspace   string `json:"workspace,omitempty"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status,omitempty"`
	IsDefault   bool   `json:"is_default,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

func (p *Project) GetName() string                 { return p.Name }
func (p *Project) GetWorkspace() string            { return p.Workspace }
func (p *Project) GetLabels() map[string]string    { return nil }
func (p *Project) SetLabels(map[string]string)     {}
func (p *Project) GetAnnotations() map[string]string { return nil }
func (p *Project) SetAnnotations(map[string]string) {}
func (p *Project) GetCreationTimestamp() string    { return p.CreatedAt }
func (p *Project) GetUpdateTimestamp() string      { return p.UpdatedAt }
func (p *Project) GetDeletionTimestamp() string    { return "" }
func (p *Project) GetSpec() interface{}             { return nil }
func (p *Project) GetStatus() interface{}           { return p.Status }
func (p *Project) GetKind() string                  { return "Project" }
func (p *Project) SetKind(string)                   {}
func (p *Project) GetID() string                    { return p.ID }
func (p *Project) GetMetadata() interface{}        { return nil }

type ProjectList struct {
	Kind  string    `json:"kind"`
	Items []Project `json:"items"`
}

func (p *ProjectList) GetKind() string { return p.Kind }
func (p *ProjectList) SetKind(kind string) { p.Kind = kind }
func (p *ProjectList) GetItems() []scheme.Object {
	items := make([]scheme.Object, len(p.Items))
	for i := range p.Items { items[i] = &p.Items[i] }
	return items
}
func (p *ProjectList) SetItems(objects []scheme.Object) {
	p.Items = make([]Project, len(objects))
	for i, object := range objects { p.Items[i] = *object.(*Project) }
}
