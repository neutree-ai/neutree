package v1

import "strconv"

type ModelSpec struct {
	Registry string `json:"registry,omitempty"`
	Name     string `json:"name,omitempty"`
	File     string `json:"file,omitempty"`
	Version  string `json:"version,omitempty"`
	Task     string `json:"task,omitempty"`
}

type EndpointEngineSpec struct {
	Engine  string `json:"engine,omitempty"`
	Version string `json:"version,omitempty"`
}

type ResourceSpec struct {
	CPU         *float64           `json:"cpu,omitempty"`
	GPU         *float64           `json:"gpu,omitempty"`
	Accelerator map[string]float64 `json:"accelerator,omitempty"`
	Memory      *float64           `json:"memory,omitempty"`
}

type ReplicaSpec struct {
	Num *int `json:"num,omitempty"`
}

type EndpointSpec struct {
	Cluster           string                 `json:"cluster,omitempty"`
	Model             *ModelSpec             `json:"model,omitempty"`
	Engine            *EndpointEngineSpec    `json:"engine,omitempty"`
	Resources         *ResourceSpec          `json:"resources,omitempty"`
	Replicas          ReplicaSpec            `json:"replicas,omitempty"`
	DeploymentOptions map[string]interface{} `json:"deployment_options,omitempty"`
	Variables         map[string]interface{} `json:"variables,omitempty"`
}

type EndpointPhase string

const (
	EndpointPhasePENDING EndpointPhase = "Pending"
	EndpointPhaseRUNNING EndpointPhase = "Running"
	EndpointPhaseFAILED  EndpointPhase = "Failed"
	EndpointPhaseDELETED EndpointPhase = "Deleted"
)

type EndpointStatus struct {
	Phase              EndpointPhase `json:"phase,omitempty"`
	ServiceURL         string        `json:"service_url,omitempty"`
	LastTransitionTime string        `json:"last_transition_time,omitempty"`
	ErrorMessage       string        `json:"error_message,omitempty"`
}

type Endpoint struct {
	ID         int             `json:"id,omitempty"`
	APIVersion string          `json:"api_version,omitempty"`
	Kind       string          `json:"kind,omitempty"`
	Metadata   *Metadata       `json:"metadata,omitempty"`
	Spec       *EndpointSpec   `json:"spec,omitempty"`
	Status     *EndpointStatus `json:"status,omitempty"`
}

func (e Endpoint) Key() string {
	if e.Metadata == nil {
		return "default" + "-" + "endpint" + "-" + strconv.Itoa(e.ID)
	}

	if e.Metadata.Workspace == "" {
		return "default" + "-" + "endpint" + "-" + strconv.Itoa(e.ID) + "-" + e.Metadata.Name
	}

	return e.Metadata.Workspace + "-" + "endpint" + "-" + strconv.Itoa(e.ID) + "-" + e.Metadata.Name
}
