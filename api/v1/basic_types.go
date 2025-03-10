package v1

type Metadata struct {
	DeletionTimestamp string            `json:"deletion_timestamp,omitempty"`
	CreationTimestamp string            `json:"creation_timestamp,omitempty"`
	UpdateTimestamp   string            `json:"update_timestamp,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
	Name              string            `json:"name,omitempty"`
}
