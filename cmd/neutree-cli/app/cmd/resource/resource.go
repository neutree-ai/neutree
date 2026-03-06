package resource

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/neutree-ai/neutree/pkg/scheme"
)

// KindPriority defines the topological apply order.
// Lower values are applied first. For delete, reverse this order.
var KindPriority = map[string]int{
	"Workspace":        0,
	"Engine":           1,
	"ImageRegistry":    1,
	"ModelRegistry":    1,
	"Role":             1,
	"OEMConfig":        1,
	"Cluster":          2,
	"Endpoint":         3,
	"ExternalEndpoint": 3,
	"ModelCatalog":     3,
	"RoleAssignment":   3,
}

// ParseMultiDocYAML splits a multi-document YAML file and decodes each document
// into the corresponding Go type via the scheme decoder.
func ParseMultiDocYAML(data []byte, decoder scheme.Decoder) ([]scheme.Object, error) {
	var resources []scheme.Object

	yamlDecoder := yaml.NewDecoder(bytes.NewReader(data))

	for {
		var raw map[string]any
		if err := yamlDecoder.Decode(&raw); err != nil {
			if err == io.EOF {
				break
			}

			return nil, fmt.Errorf("failed to decode YAML document: %w", err)
		}

		if len(raw) == 0 {
			continue
		}

		// Normalize legacy field names
		if v, ok := raw["apiVersion"]; ok {
			if _, exists := raw["api_version"]; !exists {
				raw["api_version"] = v
				delete(raw, "apiVersion")
			}
		}

		jsonData, err := json.Marshal(raw)
		if err != nil {
			return nil, fmt.Errorf("failed to convert YAML to JSON: %w", err)
		}

		obj, err := decoder.Decode(jsonData, "")
		if err != nil {
			return nil, fmt.Errorf("failed to decode resource: %w", err)
		}

		resources = append(resources, obj)
	}

	return resources, nil
}

// SortByPriority sorts resources by their dependency priority (stable sort).
func SortByPriority(resources []scheme.Object) {
	sort.SliceStable(resources, func(i, j int) bool {
		return PriorityOf(resources[i].GetKind()) < PriorityOf(resources[j].GetKind())
	})
}

// SortByReversePriority sorts resources in reverse dependency order (dependents first).
func SortByReversePriority(resources []scheme.Object) {
	sort.SliceStable(resources, func(i, j int) bool {
		return PriorityOf(resources[i].GetKind()) > PriorityOf(resources[j].GetKind())
	})
}

// PriorityOf returns the priority of a resource kind. Unknown kinds go last.
func PriorityOf(kind string) int {
	if p, ok := KindPriority[kind]; ok {
		return p
	}

	return 99
}

// Label builds a display label like "Kind/workspace/name" or "Kind/name".
func Label(kind, workspace, name string) string {
	if workspace != "" {
		return kind + "/" + workspace + "/" + name
	}

	return kind + "/" + name
}
