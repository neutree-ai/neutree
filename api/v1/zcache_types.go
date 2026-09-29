package v1

import "encoding/json"

// ZCacheSpec describes the cluster's shared L1 runtime. An explicit node list
// avoids silently deploying privileged runtime pods to newly joined nodes.
type ZCacheSpec struct {
	Enabled     bool     `json:"enabled" yaml:"enabled"`
	L1SizeGiB   int32    `json:"l1_size_gib" yaml:"l1_size_gib"`
	TargetNodes []string `json:"target_nodes" yaml:"target_nodes"`
}

// ZCacheStatus separates observations from submission/execution results. A
// failed apply must never erase the last successfully observed node inventory.
type ZCacheStatus struct {
	Phase                    string                 `json:"phase"`
	Message                  string                 `json:"message,omitempty"`
	ObservedAt               string                 `json:"observed_at,omitempty"`
	ObservationError         string                 `json:"observation_error,omitempty"`
	Current                  *ZCacheSpec            `json:"current,omitempty"`
	ConfiguredRuntimeVersion string                 `json:"configured_runtime_version,omitempty"`
	RuntimeEndpoint          *ZCacheRuntimeEndpoint `json:"runtime_endpoint,omitempty"`
	Nodes                    []ZCacheNode           `json:"nodes"`
	Candidates               []ZCacheCandidate      `json:"candidates"`
	Change                   *ZCacheChange          `json:"change,omitempty"`
	Operations               []ZCacheOperation      `json:"operations"`
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
