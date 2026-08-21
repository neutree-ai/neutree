package packageimport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAggregateSupportedTasks(t *testing.T) {
	tests := []struct {
		name string
		em   *EngineMetadata
		want []string
	}{
		{
			name: "nil top + nil versions returns nil",
			em: &EngineMetadata{
				SupportedTasks: nil,
				EngineVersions: nil,
			},
			want: nil,
		},
		{
			name: "empty everywhere returns nil",
			em: &EngineMetadata{
				SupportedTasks: []string{},
				EngineVersions: []*v1.EngineVersion{
					{Version: "v1", SupportedTasks: []string{}},
				},
			},
			want: nil,
		},
		{
			name: "top-level only",
			em: &EngineMetadata{
				SupportedTasks: []string{"a", "b"},
			},
			want: []string{"a", "b"},
		},
		{
			name: "version-level only with dedup across versions, preserves first occurrence",
			em: &EngineMetadata{
				EngineVersions: []*v1.EngineVersion{
					{Version: "v1", SupportedTasks: []string{"a"}},
					{Version: "v2", SupportedTasks: []string{"b", "a"}},
				},
			},
			want: []string{"a", "b"},
		},
		{
			name: "top + versions, top takes precedence in ordering",
			em: &EngineMetadata{
				SupportedTasks: []string{"a"},
				EngineVersions: []*v1.EngineVersion{
					{Version: "v1", SupportedTasks: []string{"b", "a"}},
					{Version: "v2", SupportedTasks: []string{"c"}},
				},
			},
			want: []string{"a", "b", "c"},
		},
		{
			name: "skips empty and whitespace-only strings",
			em: &EngineMetadata{
				SupportedTasks: []string{"a", "", "  ", "b"},
				EngineVersions: []*v1.EngineVersion{
					{Version: "v1", SupportedTasks: []string{"", "c"}},
				},
			},
			want: []string{"a", "b", "c"},
		},
		{
			name: "tolerates a nil EngineVersion entry in the slice",
			em: &EngineMetadata{
				SupportedTasks: []string{"a"},
				EngineVersions: []*v1.EngineVersion{
					nil,
					{Version: "v1", SupportedTasks: []string{"b"}},
				},
			},
			want: []string{"a", "b"},
		},
		{
			name: "second version cannot reorder tasks that the first already introduced",
			em: &EngineMetadata{
				EngineVersions: []*v1.EngineVersion{
					{Version: "v1", SupportedTasks: []string{"a", "b"}},
					{Version: "v2", SupportedTasks: []string{"b", "c"}},
				},
			},
			want: []string{"a", "b", "c"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := aggregateSupportedTasks(tt.em)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestValidateModelTasks(t *testing.T) {
	tests := []struct {
		name        string
		em          *EngineMetadata
		expectError bool
		errorParts  []string // substrings that must appear in the error message
	}{
		{
			name: "all known tasks at top + version → ok",
			em: &EngineMetadata{
				Name:           "vllm",
				SupportedTasks: []string{v1.TextGenerationModelTask},
				EngineVersions: []*v1.EngineVersion{
					{Version: "v1", SupportedTasks: []string{v1.TextEmbeddingModelTask, v1.TextRerankModelTask}},
				},
			},
			expectError: false,
		},
		{
			name: "empty everywhere → ok (validation does not require any tasks)",
			em: &EngineMetadata{
				Name: "vllm",
				EngineVersions: []*v1.EngineVersion{
					{Version: "v1"},
				},
			},
			expectError: false,
		},
		{
			name: "unknown task at top-level → error names the offending value",
			em: &EngineMetadata{
				Name:           "vllm",
				SupportedTasks: []string{"chat"},
			},
			expectError: true,
			errorParts:  []string{"chat", "engines[vllm]"},
		},
		{
			name: "unknown task at version-level → error names version + value",
			em: &EngineMetadata{
				Name: "vllm",
				EngineVersions: []*v1.EngineVersion{
					{Version: "v1.0.0", SupportedTasks: []string{"text-generation", "embedding"}},
				},
			},
			expectError: true,
			errorParts:  []string{"embedding", "v1.0.0"},
		},
		{
			name: "multiple unknown values are all reported",
			em: &EngineMetadata{
				Name:           "vllm",
				SupportedTasks: []string{"chat"},
				EngineVersions: []*v1.EngineVersion{
					{Version: "v1.0.0", SupportedTasks: []string{"speech"}},
				},
			},
			expectError: true,
			errorParts:  []string{"chat", "speech"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateModelTasks(tt.em)
			if !tt.expectError {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			for _, part := range tt.errorParts {
				assert.Contains(t, err.Error(), part)
			}
		})
	}
}

func TestUnionStrings(t *testing.T) {
	tests := []struct {
		name     string
		existing []string
		incoming []string
		want     []string
	}{
		{
			name:     "both nil returns nil",
			existing: nil,
			incoming: nil,
			want:     nil,
		},
		{
			name:     "preserves existing order, appends only new uniques",
			existing: []string{"chat", "embedding"},
			incoming: []string{"embedding", "rerank", "chat"},
			want:     []string{"chat", "embedding", "rerank"},
		},
		{
			name:     "empty existing returns dedup of incoming",
			existing: nil,
			incoming: []string{"a", "b", "a"},
			want:     []string{"a", "b"},
		},
		{
			name:     "empty incoming returns existing as-is",
			existing: []string{"a", "b"},
			incoming: nil,
			want:     []string{"a", "b"},
		},
		{
			name:     "skips empty/whitespace from incoming",
			existing: []string{"a"},
			incoming: []string{"", "  ", "b"},
			want:     []string{"a", "b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := unionStrings(tt.existing, tt.incoming)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIsManifestFile(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected bool
	}{
		{"yaml extension", "manifest.yaml", true},
		{"yml extension", "manifest.yml", true},
		{"uppercase YAML", "manifest.YAML", true},
		{"uppercase YML", "manifest.YML", true},
		{"mixed case Yaml", "manifest.Yaml", true},
		{"tar.gz extension", "package.tar.gz", false},
		{"json extension", "config.json", false},
		{"empty string", "", false},
		{"no extension", "manifest", false},
		{"yaml in path not extension", "/path/to/yaml/file.tar.gz", false},
		{"full path yaml", "/path/to/manifest.yaml", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, isManifestFile(tt.path))
		})
	}
}

func TestExtractorValidation(t *testing.T) {
	extractor := NewExtractor()

	// Test invalid package format
	err := extractor.Extract("invalid.xyz", "/tmp/test")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported package format")
}

func TestImportOptionsValidation(t *testing.T) {
	importer := NewImporter(nil)

	tests := []struct {
		name        string
		opts        *ImportOptions
		setupFunc   func() string // Returns temp file path
		cleanupFunc func(string)
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid options with skip image push",
			setupFunc: func() string {
				tmpFile, _ := os.CreateTemp("", "test-*.tar.gz")
				tmpFile.Close()
				return tmpFile.Name()
			},
			cleanupFunc: func(path string) {
				os.Remove(path)
			},
			opts: &ImportOptions{
				PackagePath:   "", // Will be set by setupFunc
				SkipImagePush: true,
			},
			expectError: false,
		},
		{
			name: "missing package path",
			opts: &ImportOptions{
				PackagePath: "",
			},
			expectError: true,
			errorMsg:    "package path is required",
		},
		{
			name: "package file not found",
			opts: &ImportOptions{
				PackagePath: "/nonexistent/package.tar.gz",
			},
			expectError: true,
			errorMsg:    "package file not found",
		},
		{
			name: "with registry when not skipping push",
			setupFunc: func() string {
				tmpFile, _ := os.CreateTemp("", "test-*.tar.gz")
				tmpFile.Close()
				return tmpFile.Name()
			},
			cleanupFunc: func(path string) {
				os.Remove(path)
			},
			opts: &ImportOptions{
				PackagePath:      "", // Will be set by setupFunc
				SkipImagePush:    false,
				MirrorRegistry:   "registry.example.com",
				RegistryUser:     "user",
				RegistryPassword: "pass",
				Workspace:        "default",
			},
			expectError: false,
		},
		{
			name: "with mirror registry when not skipping push",
			setupFunc: func() string {
				tmpFile, _ := os.CreateTemp("", "test-*.tar.gz")
				tmpFile.Close()
				return tmpFile.Name()
			},
			cleanupFunc: func(path string) {
				os.Remove(path)
			},
			opts: &ImportOptions{
				PackagePath:      "", // Will be set by setupFunc
				SkipImagePush:    false,
				MirrorRegistry:   "registry.mirror.com",
				RegistryUser:     "user",
				RegistryPassword: "pass",
			},
			expectError: false,
		},
		{
			name: "with mirror registry and registry project when not skipping push",
			setupFunc: func() string {
				tmpFile, err := os.CreateTemp("", "test-*.tar.gz")
				require.NoError(t, err)
				tmpFile.Close()
				return tmpFile.Name()
			},
			cleanupFunc: func(path string) {
				os.Remove(path)
			},
			opts: &ImportOptions{
				PackagePath:      "", // Will be set by setupFunc
				SkipImagePush:    false,
				MirrorRegistry:   "registry.mirror.com",
				RegistryProject:  "neutree-ai",
				RegistryUser:     "user",
				RegistryPassword: "pass",
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setupFunc != nil {
				tmpPath := tt.setupFunc()
				tt.opts.PackagePath = tmpPath
				if tt.cleanupFunc != nil {
					defer tt.cleanupFunc(tmpPath)
				}
			}

			err := importer.validateOptions(tt.opts)
			if tt.expectError {
				require.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidatePackageWithManifest(t *testing.T) {
	validManifest := `
manifest_version: "1.0"

engines:
- name: vllm
  engine_versions:
  - version: "v0.10.2"
    supported_tasks:
      - "text-generation"
    images:
      nvidia_gpu:
        image_name: "vllm"
        tag: "v0.10.2"
`

	t.Run("valid manifest file", func(t *testing.T) {
		dir := t.TempDir()
		manifestPath := dir + "/manifest.yaml"
		err := os.WriteFile(manifestPath, []byte(validManifest), 0644)
		require.NoError(t, err)

		validator := NewValidator()
		err = validator.ValidatePackage(manifestPath)
		assert.NoError(t, err)
	})

	t.Run("valid manifest with yml extension", func(t *testing.T) {
		dir := t.TempDir()
		manifestPath := dir + "/manifest.yml"
		err := os.WriteFile(manifestPath, []byte(validManifest), 0644)
		require.NoError(t, err)

		validator := NewValidator()
		err = validator.ValidatePackage(manifestPath)
		assert.NoError(t, err)
	})

	t.Run("invalid manifest content", func(t *testing.T) {
		dir := t.TempDir()
		manifestPath := dir + "/invalid.yaml"
		err := os.WriteFile(manifestPath, []byte("invalid: [unclosed"), 0644)
		require.NoError(t, err)

		validator := NewValidator()
		err = validator.ValidatePackage(manifestPath)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to parse manifest file")
	})

	t.Run("manifest missing engine name", func(t *testing.T) {
		manifest := `
manifest_version: "1.0"
engines:
- name: ""
  engine_versions:
  - version: "v1.0.0"
`
		dir := t.TempDir()
		manifestPath := dir + "/manifest.yaml"
		err := os.WriteFile(manifestPath, []byte(manifest), 0644)
		require.NoError(t, err)

		validator := NewValidator()
		err = validator.ValidatePackage(manifestPath)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "engine name is empty")
	})
}

func TestValidateCapabilities(t *testing.T) {
	tests := []struct {
		name        string
		em          *EngineMetadata
		expectError bool
		errorParts  []string
	}{
		{
			name: "nil metadata → ok",
			em:   nil,
		},
		{
			// A package built before the protocol declares nothing, and must
			// still import.
			name: "no declaration → ok",
			em: &EngineMetadata{
				Name:           "my-engine",
				EngineVersions: []*v1.EngineVersion{{Version: "v1"}},
			},
		},
		{
			name: "valid declaration → ok",
			em: &EngineMetadata{
				Name: "my-engine",
				EngineVersions: []*v1.EngineVersion{
					{Version: "v1", Capabilities: &v1.EngineCapabilities{
						MetricsExport: &v1.MetricsExportCapability{Enabled: true, Port: 9100, Path: "/internal/metrics"},
						Playground:    &v1.PlaygroundCapability{Enabled: true, Modes: []string{v1.PlaygroundModeChat}},
					}},
				},
			},
		},
		{
			name: "nil version entry is skipped",
			em: &EngineMetadata{
				Name:           "my-engine",
				EngineVersions: []*v1.EngineVersion{nil},
			},
		},
		{
			name: "unknown playground mode → error names the version",
			em: &EngineMetadata{
				Name: "my-engine",
				EngineVersions: []*v1.EngineVersion{
					{Version: "v2", Capabilities: &v1.EngineCapabilities{
						Playground: &v1.PlaygroundCapability{Enabled: true, Modes: []string{"vision"}},
					}},
				},
			},
			expectError: true,
			errorParts:  []string{"my-engine", "v2", "vision"},
		},
		{
			name: "metrics port out of range → error",
			em: &EngineMetadata{
				Name: "my-engine",
				EngineVersions: []*v1.EngineVersion{
					{Version: "v1", Capabilities: &v1.EngineCapabilities{
						MetricsExport: &v1.MetricsExportCapability{Enabled: true, Port: 70000},
					}},
				},
			},
			expectError: true,
			errorParts:  []string{"out of range"},
		},
		{
			// Same contract as validateModelTasks: one run reports every bad
			// version rather than making the user fix them one at a time.
			name: "every offending version is reported",
			em: &EngineMetadata{
				Name: "my-engine",
				EngineVersions: []*v1.EngineVersion{
					{Version: "v1", Capabilities: &v1.EngineCapabilities{
						Playground: &v1.PlaygroundCapability{Enabled: true, Modes: []string{"vision"}},
					}},
					{Version: "v2", Capabilities: &v1.EngineCapabilities{
						MetricsExport: &v1.MetricsExportCapability{Enabled: true, Path: "metrics"},
					}},
				},
			},
			expectError: true,
			errorParts:  []string{"v1", "v2", "vision", "must start with"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCapabilities(tt.em)

			if !tt.expectError {
				assert.NoError(t, err)
				return
			}

			require.Error(t, err)

			for _, part := range tt.errorParts {
				assert.Contains(t, err.Error(), part)
			}
		})
	}
}

func TestRegisterManifestRegistersClusterProfileAfterPackageImport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, http.MethodPost, request.Method)
		require.Equal(t, "/api/v1/clusters/profile_upsert", request.URL.Path)

		var payload struct {
			Profile *v1.ClusterProfile `json:"profile"`
		}
		require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
		require.NotNil(t, payload.Profile)
		assert.Equal(t, "v1.2.0-alpha.1", payload.Profile.GetName())
		assert.Equal(t, v1.SSHClusterType, payload.Profile.GetClusterType())
		assert.Equal(t, "neutree/neutree-serve", payload.Profile.Spec.Components.RayRuntime.Image)
		assert.Equal(t, "v1.2.0-alpha.1", payload.Profile.Spec.Components.RayRuntime.Tag)
		_, _ = writer.Write([]byte(`{"operation":"created"}`))
	}))
	defer server.Close()

	importer := NewImporter(client.NewClient(server.URL))
	result, err := importer.registerManifest(context.Background(), &ImportOptions{}, &PackageManifest{
		ClusterProfile: &ClusterProfile{
			Version:     "v1.2.0-alpha.1",
			ClusterType: v1.SSHClusterType,
			Components: ClusterProfileComponents{
				RayRuntime:       ClusterImageRef{Image: "neutree/neutree-serve", Tag: "v1.2.0-alpha.1"},
				Router:           ClusterImageRef{Image: "neutree/router", Tag: "v1.2.0-alpha.1"},
				NodeAgent:        ClusterImageRef{Image: "neutree/neutree-node-agent", Tag: "v1.2.0-alpha.1"},
				NodeExporter:     ClusterImageRef{Image: "quay.io/prometheus/node-exporter", Tag: "v1.8.2"},
				VMAgent:          ClusterImageRef{Image: "victoriametrics/vmagent", Tag: "v1.115.0"},
				KubeStateMetrics: ClusterImageRef{Image: "registry.k8s.io/kube-state-metrics/kube-state-metrics", Tag: "v2.15.0"},
			},
		},
	}, &ImportResult{})

	require.NoError(t, err)
	require.NotNil(t, result)
}
