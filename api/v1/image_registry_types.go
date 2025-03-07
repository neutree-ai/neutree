package v1

type ImageRegistryPhase string

const (
	ImageRegistryPhasePENDING   ImageRegistryPhase = "Pending"
	ImageRegistryPhaseCONNECTED ImageRegistryPhase = "Connected"
	ImageRegistryPhaseFAILED    ImageRegistryPhase = "Failed"
	ImageRegistryPhaseDELETED   ImageRegistryPhase = "Deleted"
)

type ImageRegistry struct {
	APIVersion string              `json:"api_version"`
	ID         int                 `json:"id"`
	Kind       string              `json:"kind"`
	Metadata   Metadata            `json:"metadata"`
	Spec       ImageRegistrySpec   `json:"spec"`
	Status     ImageRegistryStatus `json:"status,omitempty"`
}

type ImageRegistrySpec struct {
	AuthConfig struct {
		Password      string `json:"password,omitempty"`
		Username      string `json:"username,omitempty"`
		Auth          string `json:"auth,omitempty"`
		IdentityToken string `json:"identitytoken,omitempty"`
		RegistryToken string `json:"registrytoken,omitempty"`
	} `json:"authconfig"`
	Ca         string `json:"ca"`
	Repository string `json:"repository"`
	URL        string `json:"url"`
}

type ImageRegistryStatus struct {
	ErrorMessage       interface{}        `json:"error_message,omitempty"`
	LastTransitionTime string             `json:"last_transition_time,omitempty"`
	Phase              ImageRegistryPhase `json:"phase,omitempty"`
}
