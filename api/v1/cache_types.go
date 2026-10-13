package v1

import "encoding/json"

type ClusterCacheSpec struct {
	Provider string          `json:"provider" yaml:"provider"`
	Options  json.RawMessage `json:"options" yaml:"options"`
}

// Provider identifies the owner even after the desired configuration is removed.
type ClusterCacheStatus struct {
	Provider     string           `json:"provider"`
	Details      json.RawMessage  `json:"details,omitempty"`
	Phase        string           `json:"phase"`
	ErrorMessage string           `json:"error_message,omitempty"`
	Conditions   []CacheCondition `json:"conditions"`
}

const (
	CachePhaseDisabled        = "Disabled"
	CachePhaseReconciling     = "Reconciling"
	CachePhaseReady           = "Ready"
	CachePhaseUnavailable     = "Unavailable"
	CachePhaseFailed          = "Failed"
	CacheConfigurationApplied = "ConfigurationApplied"
	CacheReady                = "Ready"
)

type CacheCondition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message,omitempty"`
}
