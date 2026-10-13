package v1

import "encoding/json"

type ClusterCacheSpec struct {
	Provider string          `json:"provider" yaml:"provider"`
	Options  json.RawMessage `json:"options" yaml:"options"`
}

// Provider identifies the owner even after the desired configuration is removed.
type ClusterCacheStatus struct {
	Provider string          `json:"provider"`
	Details  json.RawMessage `json:"details,omitempty"`
	Error    string          `json:"error,omitempty"`
}
