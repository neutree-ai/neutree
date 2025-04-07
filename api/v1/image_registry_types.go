package v1

type ImageRegistryPhase string

const (
	ImageRegistryPhasePENDING   ImageRegistryPhase = "Pending"
	ImageRegistryPhaseCONNECTED ImageRegistryPhase = "Connected"
	ImageRegistryPhaseFAILED    ImageRegistryPhase = "Failed"
	ImageRegistryPhaseDELETED   ImageRegistryPhase = "Deleted"
)

type ImageRegistry struct {
	APIVersion string               `json:"api_version,omitempty"`
	ID         int                  `json:"id,omitempty"`
	Kind       string               `json:"kind,omitempty"`
	Metadata   *Metadata            `json:"metadata,omitempty"`
	Spec       *ImageRegistrySpec   `json:"spec,omitempty"`
	Status     *ImageRegistryStatus `json:"status,omitempty"`
}

type ImageRegistryAuthConfig struct {
	Password      string `json:"password,omitempty"`
	Username      string `json:"username,omitempty"`
	Auth          string `json:"auth,omitempty"`
	IdentityToken string `json:"identitytoken,omitempty"`
	RegistryToken string `json:"registrytoken,omitempty"`
}

type ImageRegistrySpec struct {
	AuthConfig ImageRegistryAuthConfig `json:"authconfig"`
	Ca         string                  `json:"ca"`
	Repository string                  `json:"repository"`
	URL        string                  `json:"url"`
}

type ImageRegistryStatus struct {
	ErrorMessage       string             `json:"error_message,omitempty"`
	LastTransitionTime string             `json:"last_transition_time,omitempty"`
	Phase              ImageRegistryPhase `json:"phase,omitempty"`
}
