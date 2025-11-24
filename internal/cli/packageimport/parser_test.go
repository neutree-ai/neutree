package packageimport

import (
	"os"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/assert"
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
