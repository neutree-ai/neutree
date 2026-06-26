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
