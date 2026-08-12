package packageimport

import (
	"os"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParserParseManifest(t *testing.T) {
	parser := NewParser()

	manifestContent := `
manifest_version: "1.0"

images:
    - image_name: "vllm"
      tag: "v0.10.2"
      image_file: "images/vllm.tar"

engines:
- name: vllm
  engine_versions:
  - version: "v0.10.2"

    values_schema:
      values_schema_base64: "eyJ0ZXN0IjoidmFsdWVzIn0K"
    supported_tasks:
      - "text-generation"

    images:
      nvidia_gpu:
        image_name: "vllm"
        tag: "v0.10.2"
`

	manifestContentWithoutImageTag := `
manifest_version: "1.0"

images:
    - image_name: "vllm"
      tag: "v0.10.2"
      image_file: "images/vllm.tar"

engines:
- name: vllm
  engine_versions:
  - version: "v0.10.2"

    values_schema:
      values_schema_base64: "eyJ0ZXN0IjoidmFsdWVzIn0K"
    supported_tasks:
      - "text-generation"

    images:
      nvidia_gpu:
        image_name: "vllm"
        tag: ""
`

	manifestContentWithInvalidValueScheme := `
manifest_version: "1.0"

images:
    - image_name: "vllm"
      tag: "v0.10.2"
      image_file: "images/vllm.tar"

engines:
- name: vllm
  engine_versions:
  - version: "v0.10.2"

    values_schema:
      values_schema_base64: "invalid-base64"
    supported_tasks:
      - "text-generation"

    images:
      nvidia_gpu:
        image_name: "vllm"
        tag: ""
`
	tests := []struct {
		name           string
		content        string
		expectManifest *PackageManifest
		expectError    bool
	}{
		{
			name:    "valid manifest",
			content: manifestContent,
			expectManifest: &PackageManifest{
				ManifestVersion: "1.0",
				Images: []*ImageSpec{
					{
						ImageName: "vllm",
						Tag:       "v0.10.2",
						ImageFile: "images/vllm.tar",
					},
				},
				Engines: []*EngineMetadata{
					{
						Name: "test-engine",
						EngineVersions: []*v1.EngineVersion{
							{
								Version: "v0.10.2",
								Images: map[string]*v1.EngineImage{
									"nvidia_gpu": {
										ImageName: "vllm",
										Tag:       "v0.10.2",
									},
								},
								ValuesSchema: map[string]interface{}{
									"test": "value",
								},
							},
						},
						SupportedTasks: []string{"text_generation"},
					},
				},
			},
			expectError: false,
		},
		{
			name:    "manifest with missing image tag",
			content: manifestContentWithoutImageTag,
			expectManifest: &PackageManifest{
				ManifestVersion: "1.0",
				Images: []*ImageSpec{
					{
						ImageName: "vllm",
						Tag:       "v0.10.2",
						ImageFile: "images/vllm.tar",
					},
				},
				Engines: []*EngineMetadata{
					{
						Name: "test-engine",
						EngineVersions: []*v1.EngineVersion{
							{
								Version: "v0.10.2",
								Images: map[string]*v1.EngineImage{
									"nvidia_gpu": {
										ImageName: "vllm",
										Tag:       "v0.10.2",
									},
								},
								ValuesSchema: map[string]interface{}{
									"test": "value",
								},
							},
						},
						SupportedTasks: []string{"text_generation"},
					},
				},
			},
			expectError: false,
		},

		{
			name:           "without manifest file",
			content:        "",
			expectManifest: nil,
			expectError:    true,
		},
		{
			name:           "manifest with invalid yaml",
			content:        "invalid_yaml: [unclosed_list",
			expectManifest: nil,
			expectError:    true,
		},
		{
			name:           "manifest with invalid values schema",
			content:        manifestContentWithInvalidValueScheme,
			expectManifest: nil,
			expectError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			manifestPath := dir + "/manifest.yaml"
			if tt.content != "" {
				err := os.WriteFile(manifestPath, []byte(tt.content), 0644)
				assert.NoError(t, err, "Failed to write manifest file")
			}

			manifest, err := parser.parseYAMLManifest(manifestPath)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.ObjectsAreEqual(tt.expectManifest, manifest)
			}
		})
	}
}

func TestParseManifestFile(t *testing.T) {
	parser := NewParser()

	t.Run("valid manifest with engines", func(t *testing.T) {
		content := `
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
		dir := t.TempDir()
		manifestPath := dir + "/manifest.yaml"
		err := os.WriteFile(manifestPath, []byte(content), 0644)
		require.NoError(t, err)

		manifest, err := parser.ParseManifestFile(manifestPath)
		require.NoError(t, err)
		assert.Equal(t, "1.0", manifest.ManifestVersion)
		assert.Len(t, manifest.Engines, 1)
		assert.Equal(t, "vllm", manifest.Engines[0].Name)
		assert.Len(t, manifest.Engines[0].EngineVersions, 1)
		assert.Equal(t, "v0.10.2", manifest.Engines[0].EngineVersions[0].Version)
	})

	// The capability block below is byte-for-byte what
	// scripts/builder/build-engine-package.sh emits for
	// `--metrics-export true --metrics-export-port 9100
	//  --metrics-export-path /internal/metrics --playground true
	//  --playground-modes "chat,embedding"`. Keeping the two in the same shape is
	// the point of the test: the script writes this file and the importer reads
	// it, with no schema shared between them beyond the yaml tags.
	t.Run("manifest with declared capabilities", func(t *testing.T) {
		content := `
manifest_version: "1.0"

engines:
- name: my-engine
  engine_versions:
  - version: "v1.0.0"
    supported_tasks:
      - "text-generation"

    capabilities:
      metrics_export:
        enabled: true
        port: 9100
        path: "/internal/metrics"
      playground:
        enabled: true
        modes:
        - "chat"
        - "embedding"

    images:
      nvidia_gpu:
        image_name: "my-engine"
        tag: "v1.0.0"
`
		dir := t.TempDir()
		manifestPath := dir + "/manifest.yaml"
		require.NoError(t, os.WriteFile(manifestPath, []byte(content), 0644))

		manifest, err := parser.ParseManifestFile(manifestPath)
		require.NoError(t, err)
		require.Len(t, manifest.Engines, 1)
		require.Len(t, manifest.Engines[0].EngineVersions, 1)

		version := manifest.Engines[0].EngineVersions[0]
		require.NotNil(t, version.Capabilities)

		assert.Equal(t, v1.ResolvedMetricsExport{Enabled: true, Port: 9100, Path: "/internal/metrics"},
			version.ResolveMetricsExport())
		assert.Equal(t, v1.ResolvedPlayground{Enabled: true, Modes: []string{"chat", "embedding"}},
			version.ResolvePlayground())
		assert.NoError(t, version.Capabilities.Validate())
	})

	t.Run("manifest declaring capabilities off", func(t *testing.T) {
		content := `
manifest_version: "1.0"

engines:
- name: my-engine
  engine_versions:
  - version: "v1.0.0"

    capabilities:
      metrics_export:
        enabled: false
      playground:
        enabled: false

    images:
      nvidia_gpu:
        image_name: "my-engine"
        tag: "v1.0.0"
`
		dir := t.TempDir()
		manifestPath := dir + "/manifest.yaml"
		require.NoError(t, os.WriteFile(manifestPath, []byte(content), 0644))

		manifest, err := parser.ParseManifestFile(manifestPath)
		require.NoError(t, err)

		version := manifest.Engines[0].EngineVersions[0]
		assert.False(t, version.ResolveMetricsExport().Enabled)
		assert.False(t, version.ResolvePlayground().Enabled)
	})

	// A package built before the protocol -- or by a run of the build script with
	// no capability flags -- carries no block at all, and must keep the behaviour
	// Neutree had before engines could declare anything.
	t.Run("manifest without capabilities keeps legacy behaviour", func(t *testing.T) {
		content := `
manifest_version: "1.0"

engines:
- name: my-engine
  engine_versions:
  - version: "v1.0.0"
    images:
      nvidia_gpu:
        image_name: "my-engine"
        tag: "v1.0.0"
`
		dir := t.TempDir()
		manifestPath := dir + "/manifest.yaml"
		require.NoError(t, os.WriteFile(manifestPath, []byte(content), 0644))

		manifest, err := parser.ParseManifestFile(manifestPath)
		require.NoError(t, err)

		version := manifest.Engines[0].EngineVersions[0]
		assert.Nil(t, version.Capabilities)
		assert.Equal(t, v1.ResolvedMetricsExport{
			Enabled: true,
			Port:    v1.DefaultMetricsExportPort,
			Path:    v1.DefaultMetricsExportPath,
		}, version.ResolveMetricsExport())
		assert.True(t, version.ResolvePlayground().Enabled)
	})

	t.Run("valid manifest with package_url", func(t *testing.T) {
		content := `
manifest_version: "1.0"

metadata:
  package_url: "https://example.com/package.tar.gz"

engines:
- name: vllm
  engine_versions:
  - version: "v0.10.2"
    images:
      nvidia_gpu:
        image_name: "vllm"
        tag: "v0.10.2"
`
		dir := t.TempDir()
		manifestPath := dir + "/manifest.yaml"
		err := os.WriteFile(manifestPath, []byte(content), 0644)
		require.NoError(t, err)

		manifest, err := parser.ParseManifestFile(manifestPath)
		require.NoError(t, err)
		assert.NotNil(t, manifest.Metadata)
		assert.Equal(t, "https://example.com/package.tar.gz", manifest.Metadata.PackageURL)
	})

	t.Run("invalid YAML", func(t *testing.T) {
		dir := t.TempDir()
		manifestPath := dir + "/manifest.yaml"
		err := os.WriteFile(manifestPath, []byte("invalid: [unclosed"), 0644)
		require.NoError(t, err)

		_, err = parser.ParseManifestFile(manifestPath)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to parse YAML manifest")
	})

	t.Run("missing engine name", func(t *testing.T) {
		content := `
manifest_version: "1.0"
engines:
- name: ""
  engine_versions:
  - version: "v1.0.0"
`
		dir := t.TempDir()
		manifestPath := dir + "/manifest.yaml"
		err := os.WriteFile(manifestPath, []byte(content), 0644)
		require.NoError(t, err)

		_, err = parser.ParseManifestFile(manifestPath)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "engine name is empty")
	})

	t.Run("no engine versions", func(t *testing.T) {
		content := `
manifest_version: "1.0"
engines:
- name: "vllm"
  engine_versions: []
`
		dir := t.TempDir()
		manifestPath := dir + "/manifest.yaml"
		err := os.WriteFile(manifestPath, []byte(content), 0644)
		require.NoError(t, err)

		_, err = parser.ParseManifestFile(manifestPath)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "no engine versions defined")
	})

	t.Run("invalid values_schema_base64", func(t *testing.T) {
		content := `
manifest_version: "1.0"
engines:
- name: vllm
  engine_versions:
  - version: "v0.10.2"
    values_schema:
      values_schema_base64: "not-valid-base64!!!"
    images:
      nvidia_gpu:
        image_name: "vllm"
        tag: "v0.10.2"
`
		dir := t.TempDir()
		manifestPath := dir + "/manifest.yaml"
		err := os.WriteFile(manifestPath, []byte(content), 0644)
		require.NoError(t, err)

		_, err = parser.ParseManifestFile(manifestPath)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to parse YAML manifest")
	})

	t.Run("missing manifest_version", func(t *testing.T) {
		content := `
engines:
- name: vllm
  engine_versions:
  - version: "v0.10.2"
`
		dir := t.TempDir()
		manifestPath := dir + "/manifest.yaml"
		err := os.WriteFile(manifestPath, []byte(content), 0644)
		require.NoError(t, err)

		_, err = parser.ParseManifestFile(manifestPath)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "manifest_version is required")
	})

	t.Run("no engines", func(t *testing.T) {
		content := `
manifest_version: "1.0"
engines: []
`
		dir := t.TempDir()
		manifestPath := dir + "/manifest.yaml"
		err := os.WriteFile(manifestPath, []byte(content), 0644)
		require.NoError(t, err)

		_, err = parser.ParseManifestFile(manifestPath)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "at least one engine entry is required")
	})

	t.Run("file not found", func(t *testing.T) {
		_, err := parser.ParseManifestFile("/nonexistent/manifest.yaml")
		assert.Error(t, err)
	})
}

func TestParserValidateManifest(t *testing.T) {
	dir := t.TempDir()
	err := os.MkdirAll(dir+"/images", os.ModePerm)
	assert.NoError(t, err, "Failed to create test images directory")

	// Create a dummy image file for testing
	dummyImagePath := dir + "/images/test.tar"
	_, err = os.Create(dummyImagePath)
	assert.NoError(t, err, "Failed to create dummy image file")
	parser := NewParser()

	tests := []struct {
		name        string
		manifest    *PackageManifest
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid manifest",
			manifest: &PackageManifest{
				ManifestVersion: "1.0",
				Images: []*ImageSpec{
					{
						ImageName: "test/image",
						Tag:       "v1.0.0",
						ImageFile: "images/test.tar",
					},
				},
				Engines: []*EngineMetadata{

					{
						Name: "test-engine",
						EngineVersions: []*v1.EngineVersion{
							{
								Version: "v1.0.0",
								Images: map[string]*v1.EngineImage{
									"nvidia_gpu": {
										ImageName: "vllm",
										Tag:       "v0.8.5",
									},
								},
							},
						},
						SupportedTasks: []string{v1.TextGenerationModelTask},
					},
				},
			},
			expectError: false,
		},
		{
			name: "missing image name",
			manifest: &PackageManifest{
				ManifestVersion: "1.0",
				Images: []*ImageSpec{
					{
						ImageName: "",
						Tag:       "v1.0.0",
						ImageFile: "images/test.tar",
					},
				},
				Engines: []*EngineMetadata{
					{
						Name: "test-engine",
						EngineVersions: []*v1.EngineVersion{
							{
								Version: "v1.0.0",
							},
						},
					},
				},
			},
			expectError: true,
			errorMsg:    "image 0: image name is empty",
		},
		{
			name: "missing image file",
			manifest: &PackageManifest{
				ManifestVersion: "1.0",
				Images: []*ImageSpec{
					{
						ImageName: "test/image",
						Tag:       "v1.0.0",
						ImageFile: "images/test-no-image-file.tar",
					},
				},
				Engines: []*EngineMetadata{
					{
						Name: "test-engine",
						EngineVersions: []*v1.EngineVersion{
							{
								Version: "v1.0.0",
							},
						},
					},
				},
			},
			expectError: true,
			errorMsg:    "image file not found",
		},
		{
			name: "missing engine name",
			manifest: &PackageManifest{
				ManifestVersion: "1.0",
				Images: []*ImageSpec{
					{
						ImageName: "test/image",
						Tag:       "v1.0.0",
						ImageFile: "images/test.tar",
					},
				},
				Engines: []*EngineMetadata{
					{
						Name: "",
						EngineVersions: []*v1.EngineVersion{
							{
								Version: "v1.0.0",
							},
						},
					},
				},
			},
			expectError: true,
			errorMsg:    "engine name is empty",
		},
		{
			name: "no images",
			manifest: &PackageManifest{
				ManifestVersion: "1.0",
				Images:          []*ImageSpec{},
				Engines: []*EngineMetadata{
					{
						Name: "test-engine",
						EngineVersions: []*v1.EngineVersion{
							{
								Version: "v1.0.0",
							},
						},
					},
				},
			},
			expectError: true,
			errorMsg:    "no images specified",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			err := parser.validateManifest(tt.manifest, dir)
			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
