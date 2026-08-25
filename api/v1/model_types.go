package v1

const (
	LatestVersion = "latest"
)

type ModelVersion struct {
	Name         string `json:"name"`
	CreationTime string `json:"creation_time"`
	Size         string `json:"size,omitempty"`
	Module       string `json:"module,omitempty"`
	// Labels carries the labels written into the model's own descriptor
	// (model.yaml for a BentoML-layout registry).
	Labels map[string]string `json:"labels,omitempty"`
	// Description is declared but not yet written by anything. It is reserved for
	// a model-supplied description; nothing populates it today.
	Description string `json:"description,omitempty"`
	// Alias is the display name a user gave this version, if any. It is purely a
	// label: it never reaches spec.model.name, so it never becomes the name a
	// model is served under and never affects a stored path.
	Alias string `json:"alias,omitempty"`
	// Info is what the checkpoint states about itself, plus any hand-filled
	// values. Only the detail read path fills it in — listings leave it nil
	// rather than open every checkpoint.
	Info *ModelInfo `json:"info,omitempty"`
}

type GeneralModel struct {
	Name     string         `json:"name"`
	Versions []ModelVersion `json:"versions"`
}

// ValidateModelName enforces BentoML v1.4.6 tag name rules without BentoML's
// implicit lowercasing, so API/CLI input stays consistent with stored names.
//
// Those rules happen to coincide with the platform-wide identity name contract,
// so this delegates to ValidateResourceName rather than restating them. If
// BentoML's tag rules ever diverge from what a Neutree identity may contain,
// fork this back into its own regex instead of loosening the shared one.
func ValidateModelName(name string) error {
	return ValidateResourceName("model", name)
}
