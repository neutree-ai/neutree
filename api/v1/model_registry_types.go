package v1

import "strconv"

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

type BentoMLModelRegistryConnectType string

const (
	BentoMLModelRegistryConnectTypeNFS  = "nfs"
	BentoMLModelRegistryConnectTypeFile = "file"
)

// The environment variable name for model registry
const (
	HFHomeEnv  = "HF_HOME"
	HFTokenEnv = "HF_TOKEN"
	HFEndpoint = "HF_ENDPOINT"

	BentoMLHomeEnv = "BENTOML_HOME"
)

type ModelRegistrySpec struct {
	Type        ModelRegistryType `json:"type"` // only support 'bentoml' | 'hugging-face'
	Url         string            `json:"url"`  // only support 'file://path/to/model' | 'https://huggingface.co' | 'nfs://path/to/model';
	Credentials string            `json:"credentials"`
}

type ModelRegistryStatus struct {
	ErrorMessage       string             `json:"error_message,omitempty"`
	LastTransitionTime string             `json:"last_transition_time,omitempty"`
	Phase              ModelRegistryPhase `json:"phase,omitempty"`
}

type ModelRegistry struct {
	APIVersion string               `json:"api_version,omitempty"`
	ID         int                  `json:"id,omitempty"`
	Kind       string               `json:"kind,omitempty"`
	Metadata   *Metadata            `json:"metadata,omitempty"`
	Spec       *ModelRegistrySpec   `json:"spec,omitempty"`
	Status     *ModelRegistryStatus `json:"status,omitempty"`
}

func (r ModelRegistry) Key() string {
	if r.Metadata == nil {
		return "default" + "-" + "modelregistry" + "-" + strconv.Itoa(r.ID)
	}

	if r.Metadata.Workspace == "" {
		return "default" + "-" + "modelregistry" + "-" + strconv.Itoa(r.ID) + "-" + r.Metadata.Name
	}

	return r.Metadata.Workspace + "-" + "modelregistry" + "-" + strconv.Itoa(r.ID) + "-" + r.Metadata.Name
}

func (obj *ModelRegistry) GetName() string {
	if obj.Metadata == nil {
		return ""
	}
	return obj.Metadata.Name
}

func (obj *ModelRegistry) SetName(name string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}
	obj.Metadata.Name = name
}

func (obj *ModelRegistry) GetWorkspace() string {
	if obj.Metadata == nil {
		return ""
	}
	return obj.Metadata.Workspace
}

func (obj *ModelRegistry) SetWorkspace(workspace string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}
	obj.Metadata.Workspace = workspace
}

func (obj *ModelRegistry) GetLabels() map[string]string {
	if obj.Metadata == nil {
		return nil
	}
	return obj.Metadata.Labels
}

func (obj *ModelRegistry) SetLabels(labels map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}
	obj.Metadata.Labels = labels
}

func (obj *ModelRegistry) GetAnnotations() map[string]string {
	if obj.Metadata == nil {
		return nil
	}
	return obj.Metadata.Annotations
}

func (obj *ModelRegistry) SetAnnotations(annotations map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}
	obj.Metadata.Annotations = annotations
}

func (obj *ModelRegistry) GetCreationTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}
	return obj.Metadata.CreationTimestamp
}

func (obj *ModelRegistry) SetCreationTimestamp(timestamp string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}
	obj.Metadata.CreationTimestamp = timestamp
}

func (obj *ModelRegistry) GetUpdateTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}
	return obj.Metadata.UpdateTimestamp
}

func (obj *ModelRegistry) SetUpdateTimestamp(timestamp string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}
	obj.Metadata.UpdateTimestamp = timestamp
}

func (obj *ModelRegistry) GetDeletionTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}
	return obj.Metadata.DeletionTimestamp
}

func (obj *ModelRegistry) SetDeletionTimestamp(timestamp string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}
	obj.Metadata.DeletionTimestamp = timestamp
}

func (obj *ModelRegistry) GetSpec() interface{} {
	return obj.Spec
}

func (obj *ModelRegistry) SetSpec(spec interface{}) {
	obj.Spec = spec.(*ModelRegistrySpec)
}

func (obj *ModelRegistry) GetStatus() interface{} {
	return obj.Status
}

func (obj *ModelRegistry) SetStatus(status interface{}) {
	obj.Status = status.(*ModelRegistryStatus)
}

func (obj *ModelRegistry) GetKind() string {
	return obj.Kind
}

func (obj *ModelRegistry) SetKind(kind string) {
	obj.Kind = kind
}
