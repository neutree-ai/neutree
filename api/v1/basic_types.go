package v1

type Metadata struct {
	Workspace         string            `json:"workspace,omitempty"`
	DeletionTimestamp string            `json:"deletion_timestamp,omitempty"`
	CreationTimestamp string            `json:"creation_timestamp,omitempty"`
	UpdateTimestamp   string            `json:"update_timestamp,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
	Annotations       map[string]string `json:"annotations,omitempty"`
	Name              string            `json:"name"`
}

func (m Metadata) WorkspaceName() string {
	return m.Workspace + "/" + m.Name
}
