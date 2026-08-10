package util

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestMergeEngineVersion(t *testing.T) {
	tests := []struct {
		name     string
		existing *v1.EngineVersion
		new      *v1.EngineVersion
		expected *v1.EngineVersion
	}{
		{
			name: "merge images and deploy templates",
			existing: &v1.EngineVersion{
				Version: "v1.0.0",
				Images: map[string]*v1.EngineImage{
					"cpu": {ImageName: "engine-cpu", Tag: "v1.0.0"},
				},
				DeployTemplate: map[string]map[string]string{
					"kubernetes": {"default": "default"},
				},
			},
			new: &v1.EngineVersion{
				Version: "v1.0.0",
				Images: map[string]*v1.EngineImage{
					"gpu": {ImageName: "engine-gpu", Tag: "v1.0.0"},
				},
				DeployTemplate: map[string]map[string]string{
					"kubernetes": {"pd": "pd"},
				},
			},
			expected: &v1.EngineVersion{
				Version: "v1.0.0",
				Images: map[string]*v1.EngineImage{
					"cpu": {ImageName: "engine-cpu", Tag: "v1.0.0"},
					"gpu": {ImageName: "engine-gpu", Tag: "v1.0.0"},
				},
				DeployTemplate: map[string]map[string]string{
					"kubernetes": {
						"default": "default",
						"pd":      "pd",
					},
				},
			},
		},
		{
			name: "existing deploy template is nil",
			existing: &v1.EngineVersion{
				Version:        "v1.0.0",
				Images:         map[string]*v1.EngineImage{},
				DeployTemplate: nil,
			},
			new: &v1.EngineVersion{
				Version: "v1.0.0",
				Images:  nil,
				DeployTemplate: map[string]map[string]string{
					"kubernetes": {"default": "default"},
				},
			},
			expected: &v1.EngineVersion{
				Version: "v1.0.0",
				Images:  map[string]*v1.EngineImage{},
				DeployTemplate: map[string]map[string]string{
					"kubernetes": {"default": "default"},
				},
			},
		},
		{
			name: "existing images is nil",
			existing: &v1.EngineVersion{
				Version:        "v1.0.0",
				Images:         nil,
				DeployTemplate: map[string]map[string]string{},
			},
			new: &v1.EngineVersion{
				Version: "v1.0.0",
				Images: map[string]*v1.EngineImage{
					"cpu": {ImageName: "engine-cpu", Tag: "v1.0.0"},
				},
				DeployTemplate: nil,
			},
			expected: &v1.EngineVersion{
				Version: "v1.0.0",
				Images: map[string]*v1.EngineImage{
					"cpu": {ImageName: "engine-cpu", Tag: "v1.0.0"},
				},
				DeployTemplate: map[string]map[string]string{},
			},
		},
		{
			name: "merge supported tasks",
			existing: &v1.EngineVersion{
				Version:        "v1.0.0",
				Images:         map[string]*v1.EngineImage{},
				SupportedTasks: []string{v1.TextGenerationModelTask},
			},
			new: &v1.EngineVersion{
				Version:        "v1.0.0",
				Images:         map[string]*v1.EngineImage{},
				SupportedTasks: []string{v1.TextEmbeddingModelTask},
			},
			expected: &v1.EngineVersion{
				Version:        "v1.0.0",
				Images:         map[string]*v1.EngineImage{},
				SupportedTasks: []string{v1.TextGenerationModelTask, v1.TextEmbeddingModelTask},
			},
		},
		{
			name: "override values schema",
			existing: &v1.EngineVersion{
				Version:      "v1.0.0",
				Images:       map[string]*v1.EngineImage{},
				ValuesSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"param1": map[string]interface{}{"type": "string"}}},
			},
			new: &v1.EngineVersion{
				Version:      "v1.0.0",
				Images:       map[string]*v1.EngineImage{},
				ValuesSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"param2": map[string]interface{}{"type": "integer"}}},
			},
			expected: &v1.EngineVersion{
				Version:      "v1.0.0",
				Images:       map[string]*v1.EngineImage{},
				ValuesSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"param2": map[string]interface{}{"type": "integer"}}},
			},
		},
		{
			name: "no changes",
			existing: &v1.EngineVersion{
				Version: "v1.0.0",
				Images: map[string]*v1.EngineImage{
					"cpu": {ImageName: "engine-cpu", Tag: "v1.0.0"},
				},
				DeployTemplate: map[string]map[string]string{
					"kubernetes": {"default": "default"},
				},
			},
			new: &v1.EngineVersion{
				Version: "v1.0.0",
				Images: map[string]*v1.EngineImage{
					"cpu": {ImageName: "engine-cpu", Tag: "v1.0.0"},
				},
				DeployTemplate: map[string]map[string]string{
					"kubernetes": {"default": "default"},
				},
			},
			expected: &v1.EngineVersion{
				Version: "v1.0.0",
				Images: map[string]*v1.EngineImage{
					"cpu": {ImageName: "engine-cpu", Tag: "v1.0.0"},
				},
				DeployTemplate: map[string]map[string]string{
					"kubernetes": {"default": "default"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MergeEngineVersion(tt.existing, tt.new)
			// Compare engine version
			equal, _, err := JsonEqual(result, tt.expected)
			if err != nil || !equal {
				t.Errorf("Merged Engine does not match expected.\nExpected: %+v\nGot: %+v", tt.expected, result)
			}
		})
	}
}

func TestMergeEngine(t *testing.T) {
	tests := []struct {
		name     string
		existing *v1.Engine
		new      *v1.Engine
		expected *v1.Engine
	}{
		{
			name: "merge engine versions and supported tasks",
			existing: &v1.Engine{
				Metadata: &v1.Metadata{Name: "test-engine"},
				Spec: &v1.EngineSpec{
					Versions: []*v1.EngineVersion{
						{Version: "v1.0.0"},
					},
					SupportedTasks: []string{v1.TextGenerationModelTask},
				},
			},
			new: &v1.Engine{
				Metadata: &v1.Metadata{Name: "test-engine"},
				Spec: &v1.EngineSpec{
					Versions: []*v1.EngineVersion{
						{Version: "v1.1.0"},
					},
					SupportedTasks: []string{v1.TextEmbeddingModelTask},
				},
			},
			expected: &v1.Engine{
				Metadata: &v1.Metadata{Name: "test-engine"},
				Spec: &v1.EngineSpec{
					Versions: []*v1.EngineVersion{
						{Version: "v1.0.0"},
						{Version: "v1.1.0"},
					},
					SupportedTasks: []string{v1.TextGenerationModelTask, v1.TextEmbeddingModelTask},
				},
			},
		},
		{
			name: "merge with overlapping engine versions",
			existing: &v1.Engine{
				Metadata: &v1.Metadata{Name: "test-engine"},
				Spec: &v1.EngineSpec{
					Versions: []*v1.EngineVersion{
						{
							Version: "v1.0.0",
							Images: map[string]*v1.EngineImage{
								"cpu": {ImageName: "engine-cpu", Tag: "v1.0.0"},
							},
						},
					},
					SupportedTasks: []string{v1.TextGenerationModelTask},
				},
			},
			new: &v1.Engine{
				Metadata: &v1.Metadata{Name: "test-engine"},
				Spec: &v1.EngineSpec{
					Versions: []*v1.EngineVersion{
						{
							Version: "v1.0.0",
							Images: map[string]*v1.EngineImage{
								"gpu": {ImageName: "engine-gpu", Tag: "v1.0.0"},
							},
						},
					},
					SupportedTasks: []string{v1.TextEmbeddingModelTask},
				},
			},
			expected: &v1.Engine{
				Metadata: &v1.Metadata{Name: "test-engine"},
				Spec: &v1.EngineSpec{
					Versions: []*v1.EngineVersion{
						{
							Version: "v1.0.0",
							Images: map[string]*v1.EngineImage{
								"cpu": {ImageName: "engine-cpu", Tag: "v1.0.0"},
								"gpu": {ImageName: "engine-gpu", Tag: "v1.0.0"},
							},
						},
					},
					SupportedTasks: []string{v1.TextGenerationModelTask, v1.TextEmbeddingModelTask},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MergeEngine(tt.existing, tt.new)
			// Compare engine
			equal, _, err := JsonEqual(result, tt.expected)
			if err != nil || !equal {
				t.Errorf("Merged Engine does not match expected.\nExpected: %+v\nGot: %+v", tt.expected, result)
			}
		})
	}
}

// TestMergeEngineVersion_Capabilities covers the one place where capability
// merging deliberately differs from the union semantics used for SupportedTasks:
// a re-declared capability replaces the previous one, so an engine can be
// switched off after having been switched on.
func TestMergeEngineVersion_Capabilities(t *testing.T) {
	tests := []struct {
		name     string
		existing *v1.EngineVersion
		new      *v1.EngineVersion
		expected *v1.EngineCapabilities
	}{
		{
			name:     "undeclared stays undeclared",
			existing: &v1.EngineVersion{Version: "v1"},
			new:      &v1.EngineVersion{Version: "v1"},
			expected: nil,
		},
		{
			name:     "first declaration is adopted",
			existing: &v1.EngineVersion{Version: "v1"},
			new: &v1.EngineVersion{Version: "v1", Capabilities: &v1.EngineCapabilities{
				MetricsExport: &v1.MetricsExportCapability{Enabled: true, Port: 9100},
			}},
			expected: &v1.EngineCapabilities{
				MetricsExport: &v1.MetricsExportCapability{Enabled: true, Port: 9100},
			},
		},
		{
			// The whole point of not using union semantics: re-registering with
			// enabled=false must actually disable the capability.
			name: "re-declaration can disable a capability",
			existing: &v1.EngineVersion{Version: "v1", Capabilities: &v1.EngineCapabilities{
				MetricsExport: &v1.MetricsExportCapability{Enabled: true, Port: 9100},
			}},
			new: &v1.EngineVersion{Version: "v1", Capabilities: &v1.EngineCapabilities{
				MetricsExport: &v1.MetricsExportCapability{Enabled: false},
			}},
			expected: &v1.EngineCapabilities{
				MetricsExport: &v1.MetricsExportCapability{Enabled: false},
			},
		},
		{
			name: "an omitted capability is left alone",
			existing: &v1.EngineVersion{Version: "v1", Capabilities: &v1.EngineCapabilities{
				MetricsExport: &v1.MetricsExportCapability{Enabled: true},
				Playground:    &v1.PlaygroundCapability{Enabled: true, Modes: []string{v1.PlaygroundModeChat}},
			}},
			new: &v1.EngineVersion{Version: "v1", Capabilities: &v1.EngineCapabilities{
				MetricsExport: &v1.MetricsExportCapability{Enabled: false},
			}},
			expected: &v1.EngineCapabilities{
				MetricsExport: &v1.MetricsExportCapability{Enabled: false},
				Playground:    &v1.PlaygroundCapability{Enabled: true, Modes: []string{v1.PlaygroundModeChat}},
			},
		},
		{
			// Modes replace rather than accumulate, so a mode can be dropped.
			name: "modes are replaced, not unioned",
			existing: &v1.EngineVersion{Version: "v1", Capabilities: &v1.EngineCapabilities{
				Playground: &v1.PlaygroundCapability{
					Enabled: true,
					Modes:   []string{v1.PlaygroundModeChat, v1.PlaygroundModeRerank},
				},
			}},
			new: &v1.EngineVersion{Version: "v1", Capabilities: &v1.EngineCapabilities{
				Playground: &v1.PlaygroundCapability{Enabled: true, Modes: []string{v1.PlaygroundModeChat}},
			}},
			expected: &v1.EngineCapabilities{
				Playground: &v1.PlaygroundCapability{Enabled: true, Modes: []string{v1.PlaygroundModeChat}},
			},
		},
		{
			name: "declaring nothing preserves an existing declaration",
			existing: &v1.EngineVersion{Version: "v1", Capabilities: &v1.EngineCapabilities{
				MetricsExport: &v1.MetricsExportCapability{Enabled: true},
			}},
			new: &v1.EngineVersion{Version: "v1"},
			expected: &v1.EngineCapabilities{
				MetricsExport: &v1.MetricsExportCapability{Enabled: true},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MergeEngineVersion(tt.existing, tt.new)

			equal, _, err := JsonEqual(result.Capabilities, tt.expected)
			if err != nil || !equal {
				t.Errorf("Merged capabilities do not match expected.\nExpected: %+v\nGot: %+v", tt.expected, result.Capabilities)
			}
		})
	}
}
