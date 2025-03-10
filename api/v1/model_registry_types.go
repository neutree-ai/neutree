package v1

type ModelRegistryPhase string

const (
	ModelRegistryPhasePENDING   ModelRegistryPhase = "Pending"
	ModelRegistryPhaseCONNECTED ModelRegistryPhase = "Connected"
	ModelRegistryPhaseFAILED    ModelRegistryPhase = "Failed"
	ModelRegistryPhaseDELETED   ModelRegistryPhase = "Deleted"
)

type ModelRegistryType string

const (
	HuggingFaceModelRegistryType = "hugging-face"
	BentoMLModelRegistryType     = "bentoml"
)

type ModelRegistrySpec struct {
	Type        ModelRegistryType `json:"type"` // only support 'bentoml' | 'hugging-face'
	Url         string            `json:"url"`  // only support 'file://path/to/model' | 'https://huggingface.co' | 'nfs://path/to/model';
	Credentials string            `json:"credentials"`
}

type ModelRegistryStatus struct {
	ErrorMessage       interface{}        `json:"error_message,omitempty"`
	LastTransitionTime string             `json:"last_transition_time,omitempty"`
	Phase              ModelRegistryPhase `json:"phase,omitempty"`
}

type ModelRegistry struct {
	APIVersion string              `json:"api_version"`
	ID         int                 `json:"id"`
	Kind       string              `json:"kind"`
	Metadata   Metadata            `json:"metadata"`
	Spec       ModelRegistrySpec   `json:"spec"`
	Status     ModelRegistryStatus `json:"status,omitempty"`
}
