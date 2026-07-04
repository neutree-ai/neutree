package staticcomponent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func WithHashes(components []v1.NodeComponentSpec) []v1.NodeComponentSpec {
	result := make([]v1.NodeComponentSpec, len(components))
	for i, component := range components {
		result[i] = component
		result[i].ConfigHash = Hash(component)
	}

	return result
}

func Hash(component v1.NodeComponentSpec) string {
	component.ConfigHash = ""
	component.ConfigFiles = append([]v1.NodeComponentConfigFile{}, component.ConfigFiles...)

	for i := range component.ConfigFiles {
		if component.ConfigFiles[i].SkipRestartOnChange {
			component.ConfigFiles[i].Content = ""
		}
	}

	content, err := json.Marshal(component)
	if err != nil {
		return ""
	}

	sum := sha256.Sum256(content)

	return hex.EncodeToString(sum[:])
}
