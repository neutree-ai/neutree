package v1

type ModelVersion struct {
	Name         string            `json:"name"`
	CreationTime string            `json:"creation_time"`
	Size         string            `json:"size,omitempty"`
	Module       string            `json:"module,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
	Description  string            `json:"description,omitempty"`
}

type GeneralModel struct {
	Name     string         `json:"name"`
	Versions []ModelVersion `json:"versions"`
}
