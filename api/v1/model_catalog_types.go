package v1

import "strconv"

type ModelCatalogPhase string

const (
	ModelCatalogPhasePENDING ModelCatalogPhase = "Pending"
	ModelCatalogPhaseREADY   ModelCatalogPhase = "Ready"
	ModelCatalogPhaseFAILED  ModelCatalogPhase = "Failed"
	ModelCatalogPhaseDELETED ModelCatalogPhase = "Deleted"
)

type ModelCatalogSpec struct {
	Model             *ModelSpec             `json:"model,omitempty"`
	Engine            *EndpointEngineSpec    `json:"engine,omitempty"`
	Resources         *ResourceSpec          `json:"resources,omitempty"`
	Replicas          *ReplicaSepc           `json:"replicas,omitempty"`
	DeploymentOptions map[string]interface{} `json:"deployment_options,omitempty"`
	Variables         map[string]interface{} `json:"variables,omitempty"`
}

type ModelCatalogStatus struct {
	Phase              ModelCatalogPhase `json:"phase,omitempty"`
	LastTransitionTime string            `json:"last_transition_time,omitempty"`
	ErrorMessage       string            `json:"error_message,omitempty"`
}

type ModelCatalog struct {
	APIVersion string              `json:"api_version,omitempty"`
	ID         int                 `json:"id,omitempty"`
	Kind       string              `json:"kind,omitempty"`
	Metadata   *Metadata           `json:"metadata,omitempty"`
	Spec       *ModelCatalogSpec   `json:"spec,omitempty"`
	Status     *ModelCatalogStatus `json:"status,omitempty"`
}

func (r ModelCatalog) Key() string {
	if r.Metadata == nil {
		return "default" + "-" + "modelcatalog" + "-" + strconv.Itoa(r.ID)
	}

	if r.Metadata.Workspace == "" {
		return "default" + "-" + "modelcatalog" + "-" + strconv.Itoa(r.ID) + "-" + r.Metadata.Name
	}

	return r.Metadata.Workspace + "-" + "modelcatalog" + "-" + strconv.Itoa(r.ID) + "-" + r.Metadata.Name
}
