package v1

type MetadataObject interface {
	GetName() string
	SetName(name string)
	GetWorkspace() string
	SetWorkspace(workspace string)
	GetLabels() map[string]string
	SetLabels(labels map[string]string)
	GetAnnotations() map[string]string
	SetAnnotations(annotations map[string]string)
	GetCreationTimestamp() string
	SetCreationTimestamp(timestamp string)
	GetUpdateTimestamp() string
	SetUpdateTimestamp(timestamp string)
	GetDeletionTimestamp() string
	SetDeletionTimestamp(timestamp string)
}

type Metadata struct {
	Workspace         string            `json:"workspace,omitempty"`
	DeletionTimestamp string            `json:"deletion_timestamp,omitempty"`
	CreationTimestamp string            `json:"creation_timestamp,omitempty"`
	UpdateTimestamp   string            `json:"update_timestamp,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
	Annotations       map[string]string `json:"annotations,omitempty"`
	Name              string            `json:"name"`
}

func (m *Metadata) GetName() string {
	return m.Name
}

func (m *Metadata) SetName(name string) {
	m.Name = name
}

func (m *Metadata) GetWorkspace() string {
	return m.Workspace
}

func (m *Metadata) SetWorkspace(workspace string) {
	m.Workspace = workspace
}

func (m *Metadata) GetLabels() map[string]string {
	return m.Labels
}

func (m *Metadata) SetLabels(labels map[string]string) {
	m.Labels = labels
}

func (m *Metadata) GetAnnotations() map[string]string {
	if m.Annotations == nil {
		return make(map[string]string)
	}
	return m.Annotations
}

func (m *Metadata) SetAnnotations(annotations map[string]string) {
	m.Annotations = annotations
}

func (m *Metadata) GetCreationTimestamp() string {
	return m.CreationTimestamp
}

func (m *Metadata) SetCreationTimestamp(timestamp string) {
	m.CreationTimestamp = timestamp
}

func (m *Metadata) GetUpdateTimestamp() string {
	return m.UpdateTimestamp
}

func (m *Metadata) SetUpdateTimestamp(timestamp string) {
	m.UpdateTimestamp = timestamp
}

func (m *Metadata) GetDeletionTimestamp() string {
	return m.DeletionTimestamp
}

func (m *Metadata) SetDeletionTimestamp(timestamp string) {
	m.DeletionTimestamp = timestamp
}

type Spec interface {
	GetSpec() interface{}
	SetSpec(spec interface{})
}

type Status interface {
	GetStatus() interface{}
	SetStatus(status interface{})
}

type ObjectKind interface {
	GetKind() string
	SetKind(kind string)
}

type Object interface {
	ObjectKind
	MetadataObject
	Spec
	Status
}
