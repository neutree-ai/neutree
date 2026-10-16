package v1

import "encoding/json"

type ClusterCacheSpec struct {
	Provider string          `json:"provider" yaml:"provider"`
	Options  json.RawMessage `json:"options" yaml:"options"`
}

// Provider identifies the owner even after the desired configuration is removed.
// Phase is one mutually exclusive summary; Conditions are non-exclusive facts.
type ClusterCacheStatus struct {
	Provider     string           `json:"provider"`
	Details      json.RawMessage  `json:"details,omitempty"`
	Phase        string           `json:"phase"`
	ErrorMessage string           `json:"error_message,omitempty"`
	Conditions   []CacheCondition `json:"conditions"`
}

// ConfigurationApplied means NeuTree's desired configuration has taken effect.
// For enabled caching this requires runtime readiness; completed disable also counts.
// The Ready condition describes the configuration currently written to the provider:
// a rejected change can leave Ready=True, ConfigurationApplied=False and Phase=Failed.
// Phase Ready requires both conditions True, with no ongoing work or failure taking precedence.
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
