package v1

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEngineVersion_GetImageForAccelerator(t *testing.T) {
	tests := []struct {
		name            string
		engineVersion   *EngineVersion
		acceleratorType string
		expected        *EngineImage
	}{
		{
			name: "nvidia-gpu image exists",
			engineVersion: &EngineVersion{
				Version: "v0.5.0",
				Images: map[string]*EngineImage{
					"nvidia-gpu": {
						ImageName: "neutree/vllm-cuda",
						Tag:       "v0.5.0",
					},
				},
			},
			acceleratorType: "nvidia-gpu",
			expected: &EngineImage{
				ImageName: "neutree/vllm-cuda",
				Tag:       "v0.5.0",
			},
		},
		{
			name: "amd-gpu image exists",
			engineVersion: &EngineVersion{
				Version: "v0.5.0",
				Images: map[string]*EngineImage{
					"amd-gpu": {
						ImageName: "neutree/vllm-rocm",
						Tag:       "v0.5.0",
					},
				},
			},
			acceleratorType: "amd-gpu",
			expected: &EngineImage{
				ImageName: "neutree/vllm-rocm",
				Tag:       "v0.5.0",
			},
		},
		{
			name: "accelerator type not found",
			engineVersion: &EngineVersion{
				Version: "v0.5.0",
				Images: map[string]*EngineImage{
					"nvidia-gpu": {
						ImageName: "neutree/vllm-cuda",
						Tag:       "v0.5.0",
					},
				},
			},
			acceleratorType: "intel-gpu",
			expected:        nil,
		},
		{
			name: "images map is nil",
			engineVersion: &EngineVersion{
				Version: "v0.5.0",
			},
			acceleratorType: "nvidia-gpu",
			expected:        nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.engineVersion.GetImageForAccelerator(tt.acceleratorType)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestEngineVersion_GetImageForSSHAccelerator(t *testing.T) {
	sshImage := &EngineImage{
		ImageName: "neutree/engine-vllm",
		Tag:       "v0.11.2-ray2.53.0",
	}
	genericImage := &EngineImage{
		ImageName: "vllm/vllm-openai",
		Tag:       "v0.11.2",
	}

	tests := []struct {
		name            string
		engineVersion   *EngineVersion
		acceleratorType string
		expected        *EngineImage
	}{
		{
			name: "SSH-specific image exists, returns SSH image",
			engineVersion: &EngineVersion{
				Images: map[string]*EngineImage{
					"nvidia_gpu":                     genericImage,
					SSHImageKeyPrefix + "nvidia_gpu": sshImage,
				},
			},
			acceleratorType: "nvidia_gpu",
			expected:        sshImage,
		},
		{
			name: "SSH-specific image missing, falls back to generic",
			engineVersion: &EngineVersion{
				Images: map[string]*EngineImage{
					"nvidia_gpu": genericImage,
				},
			},
			acceleratorType: "nvidia_gpu",
			expected:        genericImage,
		},
		{
			name: "SSH CPU falls back to generic CPU image",
			engineVersion: &EngineVersion{
				Images: map[string]*EngineImage{
					"cpu": {ImageName: "neutree/engine-llama-cpp", Tag: "v0.3.7-ray2.53.0"},
				},
			},
			acceleratorType: "cpu",
			expected:        &EngineImage{ImageName: "neutree/engine-llama-cpp", Tag: "v0.3.7-ray2.53.0"},
		},
		{
			name: "both SSH and generic missing, returns nil",
			engineVersion: &EngineVersion{
				Images: map[string]*EngineImage{
					"nvidia_gpu": genericImage,
				},
			},
			acceleratorType: "amd_gpu",
			expected:        nil,
		},
		{
			name: "nil Images map, returns nil",
			engineVersion: &EngineVersion{
				Version: "v0.5.0",
			},
			acceleratorType: "nvidia_gpu",
			expected:        nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.engineVersion.GetImageForSSHAccelerator(tt.acceleratorType)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestEngineVersion_SetImage(t *testing.T) {
	ev := &EngineVersion{
		Version: "v0.5.0",
	}

	ev.SetImage("nvidia-gpu", "neutree/vllm-cuda", "v0.5.0")

	assert.NotNil(t, ev.Images)
	assert.NotNil(t, ev.Images["nvidia-gpu"])
	assert.Equal(t, "neutree/vllm-cuda", ev.Images["nvidia-gpu"].ImageName)
	assert.Equal(t, "v0.5.0", ev.Images["nvidia-gpu"].Tag)

	// Add another image
	ev.SetImage("amd-gpu", "neutree/vllm-rocm", "v0.5.0")

	assert.Len(t, ev.Images, 2)
	assert.NotNil(t, ev.Images["amd-gpu"])
	assert.Equal(t, "neutree/vllm-rocm", ev.Images["amd-gpu"].ImageName)
}

func TestEngineVersion_HasImageForAccelerator(t *testing.T) {
	ev := &EngineVersion{
		Version: "v0.5.0",
		Images: map[string]*EngineImage{
			"nvidia-gpu": {
				ImageName: "neutree/vllm-cuda",
				Tag:       "v0.5.0",
			},
		},
	}

	assert.True(t, ev.HasImageForAccelerator("nvidia-gpu"))
	assert.False(t, ev.HasImageForAccelerator("amd-gpu"))
	assert.False(t, ev.HasImageForAccelerator("cpu"))
}

func TestEngineImage_GetFullImagePath(t *testing.T) {
	tests := []struct {
		name              string
		image             *EngineImage
		expectedImageName string
		expectedTag       string
	}{
		{
			name: "valid image",
			image: &EngineImage{
				ImageName: "neutree/vllm-cuda",
				Tag:       "v0.5.0",
			},
			expectedImageName: "neutree/vllm-cuda",
			expectedTag:       "v0.5.0",
		},
		{
			name:              "nil image",
			image:             nil,
			expectedImageName: "",
			expectedTag:       "",
		},
		{
			name: "image without tag",
			image: &EngineImage{
				ImageName: "neutree/vllm-cuda",
			},
			expectedImageName: "neutree/vllm-cuda",
			expectedTag:       "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			imageName, tag := tt.image.GetFullImagePath()
			assert.Equal(t, tt.expectedImageName, imageName)
			assert.Equal(t, tt.expectedTag, tag)
		})
	}
}

func TestEngineVersion_GetSupportedAccelerators(t *testing.T) {
	tests := []struct {
		name          string
		engineVersion *EngineVersion
		expectedCount int
		contains      []string
	}{
		{
			name: "multiple accelerators",
			engineVersion: &EngineVersion{
				Version: "v0.5.0",
				Images: map[string]*EngineImage{
					"nvidia-gpu": {ImageName: "neutree/vllm-cuda", Tag: "v0.5.0"},
					"amd-gpu":    {ImageName: "neutree/vllm-rocm", Tag: "v0.5.0"},
					"cpu":        {ImageName: "neutree/vllm-cpu", Tag: "v0.5.0"},
				},
			},
			expectedCount: 3,
			contains:      []string{"nvidia-gpu", "amd-gpu", "cpu"},
		},
		{
			name: "single accelerator",
			engineVersion: &EngineVersion{
				Version: "v0.5.0",
				Images: map[string]*EngineImage{
					"nvidia-gpu": {ImageName: "neutree/vllm-cuda", Tag: "v0.5.0"},
				},
			},
			expectedCount: 1,
			contains:      []string{"nvidia-gpu"},
		},
		{
			name: "no images",
			engineVersion: &EngineVersion{
				Version: "v0.5.0",
			},
			expectedCount: 0,
			contains:      []string{},
		},
		{
			name: "ssh_ prefixed keys are excluded",
			engineVersion: &EngineVersion{
				Version: "v0.11.2",
				Images: map[string]*EngineImage{
					"nvidia_gpu":                     {ImageName: "vllm/vllm-openai", Tag: "v0.11.2"},
					SSHImageKeyPrefix + "nvidia_gpu": {ImageName: "neutree/engine-vllm", Tag: "v0.11.2-ray2.53.0"},
					"cpu":                            {ImageName: "neutree/llama-cpp-python", Tag: "v0.3.7"},
					SSHImageKeyPrefix + "cpu":        {ImageName: "neutree/engine-llama-cpp", Tag: "v0.3.7-ray2.53.0"},
				},
			},
			expectedCount: 2,
			contains:      []string{"nvidia_gpu", "cpu"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.engineVersion.GetSupportedAccelerators()
			assert.Len(t, result, tt.expectedCount)

			for _, accelerator := range tt.contains {
				assert.Contains(t, result, accelerator)
			}
		})
	}
}

func TestEngineVersion_GetImageForK8sAccelerator(t *testing.T) {
	k8sImage := &EngineImage{
		ImageName: "vllm/vllm-openai",
		Tag:       "v0.11.2",
	}
	genericImage := &EngineImage{
		ImageName: "neutree/engine-vllm",
		Tag:       "v0.11.2-ray2.53.0",
	}

	tests := []struct {
		name            string
		engineVersion   *EngineVersion
		acceleratorType string
		expected        *EngineImage
	}{
		{
			name: "k8s-specific image exists, returns k8s image",
			engineVersion: &EngineVersion{
				Images: map[string]*EngineImage{
					"nvidia_gpu":                     genericImage,
					K8sImageKeyPrefix + "nvidia_gpu": k8sImage,
				},
			},
			acceleratorType: "nvidia_gpu",
			expected:        k8sImage,
		},
		{
			name: "k8s-specific image missing, falls back to generic",
			engineVersion: &EngineVersion{
				Images: map[string]*EngineImage{
					"nvidia_gpu": genericImage,
				},
			},
			acceleratorType: "nvidia_gpu",
			expected:        genericImage,
		},
		{
			name: "Kubernetes CPU falls back to generic CPU image",
			engineVersion: &EngineVersion{
				Images: map[string]*EngineImage{
					"cpu": {ImageName: "neutree/engine-llama-cpp", Tag: "v0.3.7-ray2.53.0"},
				},
			},
			acceleratorType: "cpu",
			expected:        &EngineImage{ImageName: "neutree/engine-llama-cpp", Tag: "v0.3.7-ray2.53.0"},
		},
		{
			name: "neither k8s nor generic found, returns nil",
			engineVersion: &EngineVersion{
				Images: map[string]*EngineImage{
					"nvidia_gpu": genericImage,
				},
			},
			acceleratorType: "amd_gpu",
			expected:        nil,
		},
		{
			name: "only k8s_ key exists, no plain fallback",
			engineVersion: &EngineVersion{
				Images: map[string]*EngineImage{
					K8sImageKeyPrefix + "nvidia_gpu": k8sImage,
				},
			},
			acceleratorType: "nvidia_gpu",
			expected:        k8sImage,
		},
		{
			name: "nil Images map, returns nil",
			engineVersion: &EngineVersion{
				Version: "v0.5.0",
			},
			acceleratorType: "nvidia_gpu",
			expected:        nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.engineVersion.GetImageForK8sAccelerator(tt.acceleratorType)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestEngineVersion_SupportsAccelerator(t *testing.T) {
	ev := &EngineVersion{
		Version: "v0.5.0",
		Images: map[string]*EngineImage{
			"nvidia-gpu": {ImageName: "neutree/vllm-cuda", Tag: "v0.5.0"},
			"amd-gpu":    {ImageName: "neutree/vllm-rocm", Tag: "v0.5.0"},
		},
	}

	tests := []struct {
		name            string
		acceleratorType string
		expected        bool
	}{
		{"supports nvidia-gpu", "nvidia-gpu", true},
		{"supports amd-gpu", "amd-gpu", true},
		{"does not support cpu", "cpu", false},
		{"does not support intel-gpu", "intel-gpu", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ev.SupportsAccelerator(tt.acceleratorType)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsKnownModelTask(t *testing.T) {
	tests := []struct {
		task string
		want bool
	}{
		{"text-generation", true},
		{"text-embedding", true},
		{"text-rerank", true},
		{"chat", false},
		{"", false},
		{" text-generation", false}, // leading whitespace not silently accepted
		{"TEXT-GENERATION", false},  // case sensitive
	}

	for _, tt := range tests {
		t.Run(tt.task, func(t *testing.T) {
			assert.Equal(t, tt.want, IsKnownModelTask(tt.task))
		})
	}
}

func TestIsBuiltInModelDownloaderEngine(t *testing.T) {
	tests := []struct {
		name   string
		engine string
		want   bool
	}{
		{name: "vllm", engine: EngineNameVLLM, want: true},
		{name: "llama-cpp", engine: EngineNameLlamaCpp, want: true},
		{name: "sglang", engine: EngineNameSGLang, want: true},
		{name: "custom engine", engine: "custom-engine", want: false},
		{name: "empty engine", engine: "", want: false},
		{name: "case variant", engine: "VLLM", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsBuiltInModelDownloaderEngine(tt.engine))
		})
	}
}

func TestKnownModelTasks(t *testing.T) {
	got := KnownModelTasks()

	// Stable sorted order — protects callers that render the set in error
	// messages from observing different orderings across runs.
	assert.Equal(t, []string{
		TextEmbeddingModelTask,
		TextGenerationModelTask,
		TextRerankModelTask,
	}, got)

	// Length matches IsKnownModelTask truth table.
	for _, task := range got {
		assert.True(t, IsKnownModelTask(task), "KnownModelTasks must list a known task: %q", task)
	}
}

func TestIsKnownPlaygroundMode(t *testing.T) {
	tests := []struct {
		mode string
		want bool
	}{
		{PlaygroundModeChat, true},
		{PlaygroundModeEmbedding, true},
		{PlaygroundModeRerank, true},
		// A mode is not a model task: the two vocabularies are deliberately
		// separate, so task identifiers must not validate as modes.
		{TextGenerationModelTask, false},
		{"", false},
		{"Chat", false},
	}

	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			assert.Equal(t, tt.want, IsKnownPlaygroundMode(tt.mode))
		})
	}
}

func TestKnownPlaygroundModes(t *testing.T) {
	got := KnownPlaygroundModes()

	// Stable sorted order, same contract as KnownModelTasks.
	assert.Equal(t, []string{
		PlaygroundModeChat,
		PlaygroundModeEmbedding,
		PlaygroundModeRerank,
	}, got)

	for _, mode := range got {
		assert.True(t, IsKnownPlaygroundMode(mode), "KnownPlaygroundModes must list a known mode: %q", mode)
	}
}

// TestEngineVersion_ResolveMetricsExport_Undeclared pins the forward-compat
// contract: an engine version carrying no capability declaration -- which is
// every engine registered before this protocol shipped -- must resolve to the
// behaviour Neutree had then, i.e. scraped on :8000/metrics.
func TestEngineVersion_ResolveMetricsExport_Undeclared(t *testing.T) {
	legacy := ResolvedMetricsExport{
		Enabled: true,
		Port:    DefaultMetricsExportPort,
		Path:    DefaultMetricsExportPath,
	}

	tests := []struct {
		name          string
		engineVersion *EngineVersion
	}{
		{name: "nil engine version", engineVersion: nil},
		{name: "no capabilities at all", engineVersion: &EngineVersion{Version: "v1"}},
		{
			name:          "capabilities present but metrics undeclared",
			engineVersion: &EngineVersion{Version: "v1", Capabilities: &EngineCapabilities{}},
		},
		{
			name: "another capability declared, metrics still undeclared",
			engineVersion: &EngineVersion{Version: "v1", Capabilities: &EngineCapabilities{
				Playground: &PlaygroundCapability{Enabled: false},
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, legacy, tt.engineVersion.ResolveMetricsExport())
		})
	}
}

func TestEngineVersion_ResolveMetricsExport_Declared(t *testing.T) {
	tests := []struct {
		name     string
		declared *MetricsExportCapability
		want     ResolvedMetricsExport
	}{
		{
			name:     "explicitly disabled",
			declared: &MetricsExportCapability{Enabled: false},
			want:     ResolvedMetricsExport{Enabled: false, Port: DefaultMetricsExportPort, Path: DefaultMetricsExportPath},
		},
		{
			name:     "enabled, port and path defaulted",
			declared: &MetricsExportCapability{Enabled: true},
			want:     ResolvedMetricsExport{Enabled: true, Port: DefaultMetricsExportPort, Path: DefaultMetricsExportPath},
		},
		{
			name:     "enabled on a custom port and path",
			declared: &MetricsExportCapability{Enabled: true, Port: 9100, Path: "/internal/metrics"},
			want:     ResolvedMetricsExport{Enabled: true, Port: 9100, Path: "/internal/metrics"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := &EngineVersion{Version: "v1", Capabilities: &EngineCapabilities{MetricsExport: tt.declared}}
			assert.Equal(t, tt.want, ev.ResolveMetricsExport())
		})
	}
}

// TestEngineVersion_ResolvePlayground_Undeclared is the Playground half of the
// same contract: before this protocol the console showed the tab
// unconditionally, so an undeclared engine version must keep showing it, with no
// mode restriction (nil Modes => infer from the endpoint's model task).
func TestEngineVersion_ResolvePlayground_Undeclared(t *testing.T) {
	tests := []struct {
		name          string
		engineVersion *EngineVersion
	}{
		{name: "nil engine version", engineVersion: nil},
		{name: "no capabilities at all", engineVersion: &EngineVersion{Version: "v1"}},
		{
			name:          "capabilities present but playground undeclared",
			engineVersion: &EngineVersion{Version: "v1", Capabilities: &EngineCapabilities{}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.engineVersion.ResolvePlayground()
			assert.True(t, got.Enabled)
			assert.Nil(t, got.Modes)
		})
	}
}

func TestEngineVersion_ResolvePlayground_Declared(t *testing.T) {
	tests := []struct {
		name     string
		declared *PlaygroundCapability
		want     ResolvedPlayground
	}{
		{
			name:     "explicitly disabled",
			declared: &PlaygroundCapability{Enabled: false},
			want:     ResolvedPlayground{Enabled: false},
		},
		{
			name:     "enabled with no mode restriction",
			declared: &PlaygroundCapability{Enabled: true},
			want:     ResolvedPlayground{Enabled: true},
		},
		{
			// An explicit `modes: []` from JSON/YAML means the same thing as an
			// omitted one, and must resolve identically -- a non-nil empty slice
			// could be read as "supports no mode at all".
			name:     "enabled with an explicitly empty mode list",
			declared: &PlaygroundCapability{Enabled: true, Modes: []string{}},
			want:     ResolvedPlayground{Enabled: true},
		},
		{
			name:     "enabled, narrowed to chat",
			declared: &PlaygroundCapability{Enabled: true, Modes: []string{PlaygroundModeChat}},
			want:     ResolvedPlayground{Enabled: true, Modes: []string{PlaygroundModeChat}},
		},
		{
			// Disabled wins over any declared modes: consumers must gate on
			// Enabled before looking at Modes.
			name:     "disabled but modes listed",
			declared: &PlaygroundCapability{Enabled: false, Modes: []string{PlaygroundModeChat}},
			want:     ResolvedPlayground{Enabled: false, Modes: []string{PlaygroundModeChat}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := &EngineVersion{Version: "v1", Capabilities: &EngineCapabilities{Playground: tt.declared}}
			assert.Equal(t, tt.want, ev.ResolvePlayground())
		})
	}
}

func TestEngineCapabilities_Validate(t *testing.T) {
	tests := []struct {
		name         string
		capabilities *EngineCapabilities
		wantErr      string
	}{
		{name: "nil declaration is valid", capabilities: nil},
		{name: "empty declaration is valid", capabilities: &EngineCapabilities{}},
		{
			name: "fully specified declaration",
			capabilities: &EngineCapabilities{
				MetricsExport: &MetricsExportCapability{Enabled: true, Port: 9100, Path: "/metrics"},
				Playground:    &PlaygroundCapability{Enabled: true, Modes: []string{PlaygroundModeChat}},
			},
		},
		{
			// Zero means "use the default", not an invalid port.
			name:         "zero port is valid",
			capabilities: &EngineCapabilities{MetricsExport: &MetricsExportCapability{Enabled: true}},
		},
		{
			name:         "port above the valid range",
			capabilities: &EngineCapabilities{MetricsExport: &MetricsExportCapability{Enabled: true, Port: 70000}},
			wantErr:      "out of range",
		},
		{
			name:         "negative port",
			capabilities: &EngineCapabilities{MetricsExport: &MetricsExportCapability{Enabled: true, Port: -1}},
			wantErr:      "out of range",
		},
		{
			name:         "path without a leading slash",
			capabilities: &EngineCapabilities{MetricsExport: &MetricsExportCapability{Enabled: true, Path: "metrics"}},
			wantErr:      "must start with",
		},
		{
			name: "unknown playground mode",
			capabilities: &EngineCapabilities{
				Playground: &PlaygroundCapability{Enabled: true, Modes: []string{PlaygroundModeChat, "vision"}},
			},
			wantErr: "unknown mode",
		},
		{
			// Model tasks and playground modes are separate vocabularies; using
			// one where the other belongs must be caught at registration.
			name: "model task used as a playground mode",
			capabilities: &EngineCapabilities{
				Playground: &PlaygroundCapability{Enabled: true, Modes: []string{TextGenerationModelTask}},
			},
			wantErr: "unknown mode",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.capabilities.Validate()

			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}

			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}
