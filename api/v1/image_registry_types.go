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

func (obj *ImageRegistry) GetName() string {
	if obj.Metadata == nil {
		return ""
	}
	return obj.Metadata.Name
}

func (obj *ImageRegistry) SetName(name string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}
	obj.Metadata.Name = name
}

func (obj *ImageRegistry) GetWorkspace() string {
	if obj.Metadata == nil {
		return ""
	}
	return obj.Metadata.Workspace
}

func (obj *ImageRegistry) SetWorkspace(workspace string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}
	obj.Metadata.Workspace = workspace
}

func (obj *ImageRegistry) GetLabels() map[string]string {
	if obj.Metadata == nil {
		return nil
	}
	return obj.Metadata.Labels
}

func (obj *ImageRegistry) SetLabels(labels map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}
	obj.Metadata.Labels = labels
}

func (obj *ImageRegistry) GetAnnotations() map[string]string {
	if obj.Metadata == nil {
		return nil
	}
	return obj.Metadata.Annotations
}

func (obj *ImageRegistry) SetAnnotations(annotations map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}
	obj.Metadata.Annotations = annotations
}

func (obj *ImageRegistry) GetCreationTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}
	return obj.Metadata.CreationTimestamp
}

func (obj *ImageRegistry) SetCreationTimestamp(timestamp string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}
	obj.Metadata.CreationTimestamp = timestamp
}

func (obj *ImageRegistry) GetUpdateTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}
	return obj.Metadata.UpdateTimestamp
}

func (obj *ImageRegistry) SetUpdateTimestamp(timestamp string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}
	obj.Metadata.UpdateTimestamp = timestamp
}

func (obj *ImageRegistry) GetDeletionTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}
	return obj.Metadata.DeletionTimestamp
}

func (obj *ImageRegistry) SetDeletionTimestamp(timestamp string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}
	obj.Metadata.DeletionTimestamp = timestamp
}

func (obj *ImageRegistry) GetSpec() interface{} {
	return obj.Spec
}

func (obj *ImageRegistry) SetSpec(spec interface{}) {
	obj.Spec = spec.(*ImageRegistrySpec)
}

func (obj *ImageRegistry) GetStatus() interface{} {
	return obj.Status
}

func (obj *ImageRegistry) SetStatus(status interface{}) {
	obj.Status = status.(*ImageRegistryStatus)
}

func (obj *ImageRegistry) GetKind() string {
	return obj.Kind
}

func (obj *ImageRegistry) SetKind(kind string) {
	obj.Kind = kind
}
