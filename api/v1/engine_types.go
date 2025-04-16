package v1

import "time"

type EngineVersion struct {
	Version      string                 `json:"version,omitempty"`
	ValuesSchema map[string]interface{} `json:"values_schema,omitempty"`
}

type EngineSpec struct {
	Versions       []*EngineVersion `json:"versions,omitempty"`
	SupportedTasks []string         `json:"supported_tasks,omitempty"`
}

type EnginePhase string

const (
	EnginePhasePending EnginePhase = "Pending"
	EnginePhaseCreated EnginePhase = "Created"
	EnginePhaseDeleted EnginePhase = "Deleted"
	EnginePhaseFailed  EnginePhase = "Failed"
)

type EngineStatus struct {
	Phase              EnginePhase `json:"phase,omitempty"`
	LastTransitionTime *time.Time  `json:"last_transition_time,omitempty"`
	ErrorMessage       string      `json:"error_message,omitempty"`
}

type Engine struct {
	ID         int           `json:"id,omitempty"`
	APIVersion string        `json:"api_version,omitempty"`
	Kind       string        `json:"kind,omitempty"`
	Metadata   *Metadata     `json:"metadata,omitempty"`
	Spec       *EngineSpec   `json:"spec,omitempty"`
	Status     *EngineStatus `json:"status,omitempty"`
}
