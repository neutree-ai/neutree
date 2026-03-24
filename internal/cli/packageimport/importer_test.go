package packageimport

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
