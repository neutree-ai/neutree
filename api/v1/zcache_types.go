package v1

import "encoding/json"

// ZCacheSpec describes the cluster's shared L1 runtime. An explicit node list
// avoids silently deploying privileged runtime pods to newly joined nodes.
type ZCacheSpec struct {
	ControlPlane *ZCacheControlPlaneSpec `json:"control_plane,omitempty" yaml:"control_plane,omitempty"`
	Enabled      bool                    `json:"enabled" yaml:"enabled"`
	L1SizeGiB    int32                   `json:"l1_size_gib" yaml:"l1_size_gib"`
	TargetNodes  []string                `json:"target_nodes" yaml:"target_nodes"`
}

// ZCacheStatus separates observations from submission/execution results. A
// failed apply must never erase the last successfully observed node inventory.
type ZCacheStatus struct {
	ControlPlane             *ZCacheControlPlaneStatus `json:"control_plane,omitempty"`
	Phase                    string                    `json:"phase"`
	Message                  string                    `json:"message,omitempty"`
	ObservedAt               string                    `json:"observed_at,omitempty"`
	ObservationError         string                    `json:"observation_error,omitempty"`
	Current                  *ZCacheSpec               `json:"current,omitempty"`
	ConfiguredRuntimeVersion string                    `json:"configured_runtime_version,omitempty"`
	RuntimeEndpoint          *ZCacheRuntimeEndpoint    `json:"runtime_endpoint,omitempty"`
	Nodes                    []ZCacheNode              `json:"nodes"`
	Candidates               []ZCacheCandidate         `json:"candidates"`
	Change                   *ZCacheChange             `json:"change,omitempty"`
	Operations               []ZCacheOperation         `json:"operations"`
}

type ZCacheRuntimeEndpoint struct {
	Address string `json:"address"`
	Port    int32  `json:"port"`
}

type ZCacheNode struct {
	Name          string `json:"name"`
	Runtime       string `json:"runtime"`
	Cache         string `json:"cache"`
	CapacityBytes int64  `json:"capacity_bytes"`
	Reason        string `json:"reason,omitempty"`
}

type ZCacheCandidate struct {
	Name       string `json:"name"`
	Selectable bool   `json:"selectable"`
	Reason     string `json:"reason,omitempty"`
}

// ZCacheChange is a durable submission intent. Persisting the exact request
// before sending it lets a timeout or process restart reuse the same key/body.
// It contains no credentials. History is read from ZCache, not reconstructed.
type ZCacheChange struct {
	RuntimeGeneration int64           `json:"runtime_generation,omitempty"`
	SpecHash          string          `json:"spec_hash"`
	Request           json.RawMessage `json:"request"`
	OperationID       string          `json:"operation_id,omitempty"`
	Phase             string          `json:"phase"`
	Message           string          `json:"message,omitempty"`
}

type ZCacheOperation struct {
	ID          string                `json:"id"`
	Phase       string                `json:"phase"`
	Kind        string                `json:"kind"`
	CreatedAt   string                `json:"created_at,omitempty"`
	CompletedAt string                `json:"completed_at,omitempty"`
	Message     string                `json:"message,omitempty"`
	Nodes       []ZCacheOperationNode `json:"nodes"`
}

type ZCacheOperationNode struct {
	Name   string `json:"name"`
	Phase  string `json:"phase"`
	Reason string `json:"reason,omitempty"`
}

// ZCacheControlPlaneSpec is an explicit installation action. A new request ID
// authorizes one attempt, including retrying or reapplying the same version.
type ZCacheControlPlaneSpec struct {
	Version   string `json:"version" yaml:"version"`
	RequestID string `json:"request_id" yaml:"request_id"`
}

// ZCacheControlPlaneStatus keeps action results separate from current health.
// Installation inputs are frozen before Helm is invoked; a distribution update
// must not silently change an existing cluster's target or retry an old action.
type ZCacheControlPlaneStatus struct {
	Version           string                      `json:"version,omitempty"`
	TargetVersion     string                      `json:"target_version,omitempty"`
	RequestID         string                      `json:"request_id,omitempty"`
	Phase             string                      `json:"phase,omitempty"`
	Message           string                      `json:"message,omitempty"`
	StartedAt         string                      `json:"started_at,omitempty"`
	CompletedAt       string                      `json:"completed_at,omitempty"`
	Revision          int                         `json:"revision,omitempty"`
	ImagePrefix       string                      `json:"image_prefix,omitempty"`
	PullSecret        string                      `json:"pull_secret,omitempty"`
	Ready             bool                        `json:"ready"`
	HealthMessage     string                      `json:"health_message,omitempty"`
	AvailableVersions []ZCacheControlPlaneVersion `json:"available_versions"`
}

// Version metadata comes from the enabled distribution, never arbitrary tags.
type ZCacheControlPlaneVersion struct {
	Version          string   `json:"version"`
	ChartVersion     string   `json:"chart_version"`
	NodeAgentVersion string   `json:"node_agent_version"`
	RuntimeVersions  []string `json:"runtime_versions"`
	UpgradeFrom      []string `json:"upgrade_from"`
}
