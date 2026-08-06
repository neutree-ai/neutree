package v1

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	LatestVersion = "latest"

	maxModelNameLength = 63
)

var modelNameRegex = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]{0,61}[a-z0-9])?$`)

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
func ValidateModelName(name string) error {
	if name != strings.TrimSpace(name) {
		return fmt.Errorf("model name must not contain leading or trailing whitespace")
	}

	if len(name) == 0 {
		return fmt.Errorf("model name is required")
	}

	if len(name) > maxModelNameLength {
		return fmt.Errorf("model name must be at most %d characters", maxModelNameLength)
	}

	if strings.ToLower(name) != name {
		return fmt.Errorf("model name must be lowercase")
	}

	if !modelNameRegex.MatchString(name) {
		return fmt.Errorf("model name must consist of lowercase alphanumeric characters, '_', '-', or '.', and must start and end with an alphanumeric character")
	}

	return nil
}
